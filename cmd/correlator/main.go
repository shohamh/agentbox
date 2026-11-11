// correlator matches what models asked for with what actually ran in each
// sandbox and turns event patterns into alerts. It runs over ClickHouse every
// few seconds:
//   D4 sandbox token used from anywhere but its own sandbox (relay)
//   D5 model-API-shaped traffic through a preview port
//   D6 execution of a watched network tool with no matching tool call
//   D7 denied egress from a sandbox
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"agentbox/pkg/ch"
	"agentbox/pkg/db"
	"agentbox/pkg/otlp"
)

type corr struct {
	pool *pgxpool.Pool
	ch   *ch.Client
	ev   *otlp.Exporter
}

var watched = map[string]bool{"nc": true, "ncat": true, "netcat": true, "socat": true, "nmap": true, "curl": true, "wget": true, "scp": true, "ssh": true, "telnet": true}

func main() {
	ctx := context.Background()
	pool, err := db.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	db.WaitSchema(ctx, pool)
	c := &corr{pool: pool,
		ch: ch.New(os.Getenv("CLICKHOUSE_URL"), os.Getenv("CLICKHOUSE_USER"), os.Getenv("CLICKHOUSE_PASSWORD"), "otel"),
		ev: otlp.New(os.Getenv("OTLP_ENDPOINT"), map[string]string{"service.name": "correlator"}, nil)}
	go http.ListenAndServe(":8084", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) }))
	interval := 5 * time.Second
	log.Printf("correlator running every %s", interval)
	for {
		if err := c.run(ctx); err != nil {
			log.Printf("run: %v", err)
		}
		time.Sleep(interval)
	}
}

func (c *corr) watermark(ctx context.Context) time.Time {
	var t time.Time
	if err := c.pool.QueryRow(ctx, `SELECT watermark FROM correlator_state WHERE name='main'`).Scan(&t); err != nil {
		return time.Now().Add(-10 * time.Minute)
	}
	return t
}

const evq = `SELECT toString(Timestamp) AS ts, LogAttributes['event.name'] AS name,
  ResourceAttributes['agentbox.device'] AS device, ResourceAttributes['agentbox.user'] AS user,
  coalesce(nullIf(LogAttributes['agentbox.sandbox'], ''), ResourceAttributes['agentbox.sandbox']) AS sandbox,
  LogAttributes AS attrs
FROM otel_logs WHERE Timestamp > parseDateTime64BestEffort({since:String}, 9) AND Timestamp <= parseDateTime64BestEffort({until:String}, 9)
  AND LogAttributes['event.name'] IN ('token.relay','egress.denied','preview.blocked','process.exec')
ORDER BY Timestamp LIMIT 5000`

func (c *corr) run(ctx context.Context) error {
	since := c.watermark(ctx)
	// Leave time for late arrivals: tool-call records reach ClickHouse seconds after the execution they explain.
	until := time.Now().Add(-12 * time.Second)
	if !until.After(since) {
		return nil
	}
	rows, err := c.ch.Query(ctx, evq, map[string]string{"since": since.UTC().Format(time.RFC3339Nano), "until": until.UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return err
	}
	for _, r := range rows {
		name := ch.Str(r, "name")
		dev, user, sb := ch.Str(r, "device"), ch.Str(r, "user"), ch.Str(r, "sandbox")
		attrs, _ := r["attrs"].(map[string]any)
		switch name {
		case "token.relay":
			c.alert(ctx, "D4", "high", dev, user, sb, fmt.Sprintf("Sandbox token used from %s instead of its own sandbox (relay attempt)", ch.Str(attrs, "source_ip")),
				r, fmt.Sprintf("D4:%s:%s", sb, ch.Str(attrs, "source_ip")))
		case "preview.blocked":
			c.alert(ctx, "D5", "high", dev, user, sb, "Model-API-shaped request blocked on preview port "+ch.Str(attrs, "port"),
				r, fmt.Sprintf("D5:%s:%s", sb, ch.Str(attrs, "port")))
		case "egress.denied":
			host := ch.Str(attrs, "host")
			c.alert(ctx, "D7", "medium", dev, user, sb, "Sandbox tried to reach "+host+", which is outside its egress list",
				r, fmt.Sprintf("D7:%s:%s:%s", sb, host, until.Format("2006010215")))
		case "process.exec":
			bin := path.Base(ch.Str(attrs, "process.executable"))
			if watched[bin] && !c.explained(ctx, sb, bin, ch.Str(r, "ts")) {
				c.alert(ctx, "D6", "low", dev, user, sb, "Unexplained execution of "+bin+": no model tool call asked for it",
					r, fmt.Sprintf("D6:%s:%s:%s", sb, bin, until.Format("20060102")))
			}
		}
	}
	_, err = c.pool.Exec(ctx, `INSERT INTO correlator_state(name, watermark) VALUES('main',$1) ON CONFLICT(name) DO UPDATE SET watermark=$1`, until)
	return err
}

// explained reports whether a tool call in the same sandbox within the five
// minutes before ts mentions the binary (from the harness's or LiteLLM's
// tool-call records).
func (c *corr) explained(ctx context.Context, sb, bin, ts string) bool {
	q := `SELECT count() AS n FROM otel_logs
	WHERE coalesce(nullIf(LogAttributes['agentbox.sandbox'], ''), ResourceAttributes['agentbox.sandbox']) = {sb:String}
	  AND Timestamp BETWEEN parseDateTime64BestEffort({ts:String}, 9) - INTERVAL 300 SECOND AND parseDateTime64BestEffort({ts:String}, 9) + INTERVAL 2 SECOND
	  AND (LogAttributes['event.name'] IN ('gen_ai.tool_call','tool.call') OR Body LIKE 'codex.tool%')
	  AND positionCaseInsensitive(toString(LogAttributes), {bin:String}) > 0`
	rows, err := c.ch.Query(ctx, q, map[string]string{"sb": sb, "ts": ts, "bin": bin})
	if err != nil || len(rows) == 0 {
		return false
	}
	return ch.Str(rows[0], "n") != "0"
}

func (c *corr) alert(ctx context.Context, det, sev, dev, user, sb, title string, evidence map[string]any, key string) {
	ev, _ := json.Marshal(evidence)
	tag, err := c.pool.Exec(ctx, `INSERT INTO alerts(detection, severity, device_id, username, sandbox, title, evidence, dedup_key)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (dedup_key) DO NOTHING`, det, sev, dev, user, sb, title, ev, key)
	if err != nil {
		log.Printf("alert: %v", err)
		return
	}
	if tag.RowsAffected() == 0 {
		return
	}
	log.Printf("%s %s: %s", det, sev, title)
	c.ev.EmitSev("WARN", "alert.raised", map[string]any{"detection": det, "severity": sev, "agentbox.device": dev, "agentbox.user": user, "agentbox.sandbox": sb, "title": strings.TrimSpace(title)})
	db.Notify(ctx, c.pool, db.ChanConsole, "alerts")
}
