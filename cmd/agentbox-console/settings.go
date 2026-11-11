package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"agentbox/pkg/db"
	"agentbox/pkg/otlp"
)

// ---------- server-sent events ----------

type broker struct {
	mu   sync.Mutex
	subs map[chan string]bool
}

func newBroker() *broker { return &broker{subs: map[chan string]bool{}} }

func (b *broker) publish(topic string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for c := range b.subs {
		select {
		case c <- topic:
		default:
		}
	}
}

func (s *Server) events(w http.ResponseWriter, r *http.Request, _ *session) {
	fl, ok := w.(http.Flusher)
	if !ok {
		fail(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	c := make(chan string, 64)
	s.sse.mu.Lock()
	s.sse.subs[c] = true
	s.sse.mu.Unlock()
	defer func() {
		s.sse.mu.Lock()
		delete(s.sse.subs, c)
		s.sse.mu.Unlock()
	}()
	fmt.Fprint(w, "event: hello\ndata: {}\n\n")
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case t := <-c:
			fmt.Fprintf(w, "event: change\ndata: {\"topic\":%q}\n\n", t)
			fl.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// ---------- settings ----------

type Exporters struct {
	OTLPHTTP struct {
		Enabled  bool   `json:"enabled"`
		Endpoint string `json:"endpoint"`
	} `json:"otlphttp"`
	SplunkHEC struct {
		Enabled  bool   `json:"enabled"`
		Endpoint string `json:"endpoint"`
		Token    string `json:"token"`
		Index    string `json:"index"`
	} `json:"splunk_hec"`
}

var settingKeys = map[string]bool{"exporters": true, "retention": true, "reviewer": true, "revoked_releases": true}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request, ss *session) {
	out := map[string]any{}
	for k := range settingKeys {
		var v any
		if db.GetSetting(r.Context(), s.pool, k, &v) {
			out[k] = v
		}
	}
	if ex, ok := out["exporters"].(map[string]any); ok && ss.Role() != "admin" {
		if sp, ok := ex["splunk_hec"].(map[string]any); ok && sp["token"] != "" {
			sp["token"] = "********"
		}
	}
	out["sso"] = map[string]any{"issuer": s.oidc.publicBase, "client_id": s.oidc.conf.ClientID}
	writeJSON(w, 200, out)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request, ss *session) {
	key := r.PathValue("key")
	if !settingKeys[key] {
		fail(w, 404, "unknown setting")
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err := db.PutSetting(r.Context(), s.pool, key, v, ss.User); err != nil {
		fail(w, 500, err.Error())
		return
	}
	detail := map[string]any{"value": v}
	if key == "exporters" {
		var ex Exporters
		json.Unmarshal(raw, &ex)
		ex.SplunkHEC.Token = strings.Repeat("*", len(ex.SplunkHEC.Token))
		detail = map[string]any{"value": ex}
		s.writeCollectorConfig(r.Context())
	}
	if key == "retention" {
		if err := s.applyRetention(r.Context(), v); err != nil {
			detail["retention_error"] = err.Error()
		}
	}
	s.audit(r.Context(), ss.User, ss.Role(), "settings."+key, key, detail)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) applyRetention(ctx context.Context, v any) error {
	m, _ := v.(map[string]any)
	days, _ := m["events_days"].(float64)
	if days < 1 {
		return nil
	}
	_, err := s.ch.Query(ctx, fmt.Sprintf(`ALTER TABLE otel_logs MODIFY TTL TimestampTime + toIntervalDay(%d)`, int(days)), nil)
	return err
}

// writeCollectorConfig renders the exporter part of the central collector's
// configuration. The collector merges it over its base file and restarts
// when it changes, so turning Splunk on is configuration only.
func (s *Server) writeCollectorConfig(ctx context.Context) {
	if s.collectorConf == "" {
		return
	}
	var ex Exporters
	db.GetSetting(ctx, s.pool, "exporters", &ex)
	var b strings.Builder
	list := []string{"clickhouse"}
	b.WriteString("# Written by agentbox-console; edit in Settings.\nexporters:\n")
	if ex.OTLPHTTP.Enabled && ex.OTLPHTTP.Endpoint != "" {
		fmt.Fprintf(&b, "  otlphttp/custom:\n    endpoint: %q\n    encoding: json\n    compression: none\n    tls:\n      insecure: true\n    sending_queue:\n      enabled: true\n", ex.OTLPHTTP.Endpoint)
		list = append(list, "otlphttp/custom")
	}
	if ex.SplunkHEC.Enabled && ex.SplunkHEC.Endpoint != "" {
		idx := ex.SplunkHEC.Index
		if idx == "" {
			idx = "agentbox"
		}
		fmt.Fprintf(&b, "  splunk_hec/custom:\n    endpoint: %q\n    token: %q\n    index: %q\n    source: agentbox\n    sourcetype: otel\n    tls:\n      insecure_skip_verify: true\n", ex.SplunkHEC.Endpoint, ex.SplunkHEC.Token, idx)
		list = append(list, "splunk_hec/custom")
	}
	if len(list) == 1 {
		b.WriteString("  {}\n")
	}
	l := "[" + strings.Join(list, ", ") + "]"
	fmt.Fprintf(&b, "service:\n  pipelines:\n    logs:\n      exporters: %s\n    traces:\n      exporters: %s\n    metrics:\n      exporters: %s\n", l, l, l)
	tmp := s.collectorConf + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		log.Printf("collector config: %v", err)
		return
	}
	os.Rename(tmp, s.collectorConf)
	log.Printf("collector exporters: %s", l)
}

// testExporter sends one record straight to the configured destination.
func (s *Server) testExporter(w http.ResponseWriter, r *http.Request, ss *session) {
	b := body(r)
	name := str(b, "name")
	var ex Exporters
	db.GetSetting(r.Context(), s.pool, "exporters", &ex)
	rec := otlp.Record{Time: time.Now(), Severity: "INFO", Name: "agentbox.exporter_test", Attrs: map[string]any{"agentbox.test": true, "audit.actor": ss.User}}
	var req *http.Request
	switch name {
	case "otlphttp":
		if ex.OTLPHTTP.Endpoint == "" {
			fail(w, 400, "no otlphttp endpoint configured")
			return
		}
		e := otlp.New("", map[string]string{"service.name": "agentbox-console"}, nil)
		req, _ = http.NewRequest("POST", strings.TrimRight(ex.OTLPHTTP.Endpoint, "/")+"/v1/logs", bytes.NewReader(e.Payload([]otlp.Record{rec})))
		req.Header.Set("Content-Type", "application/json")
	case "splunk_hec":
		if ex.SplunkHEC.Endpoint == "" {
			fail(w, 400, "no Splunk HEC endpoint configured")
			return
		}
		ev, _ := json.Marshal(map[string]any{"event": map[string]any{"name": rec.Name, "actor": ss.User}, "sourcetype": "agentbox:test", "index": ex.SplunkHEC.Index})
		req, _ = http.NewRequest("POST", ex.SplunkHEC.Endpoint, bytes.NewReader(ev))
		req.Header.Set("Authorization", "Splunk "+ex.SplunkHEC.Token)
	default:
		fail(w, 400, "name must be otlphttp or splunk_hec")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	result := map[string]any{"exporter": name}
	if err != nil {
		result["ok"], result["error"] = false, err.Error()
	} else {
		resp.Body.Close()
		result["ok"], result["status"] = resp.StatusCode < 300, resp.Status
	}
	s.audit(r.Context(), ss.User, ss.Role(), "settings.exporter_test", name, result)
	writeJSON(w, 200, result)
}
