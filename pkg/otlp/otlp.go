// Package otlp is a small OTLP/HTTP (JSON) log exporter. Every Agentbox
// component emits its events as OpenTelemetry log records through it, so the
// same records reach ClickHouse, the console and any later SIEM.
package otlp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type Record struct {
	Time     time.Time
	Severity string
	Name     string
	Attrs    map[string]any
	TraceID  string
}

type Exporter struct {
	endpoint string // full URL, e.g. http://otel:4318/v1/logs
	resource map[string]string
	client   *http.Client
	ch       chan Record
	mu       sync.RWMutex
	wg       sync.WaitGroup
}

// New starts a background batcher. endpoint may be empty, in which case
// records are only logged locally.
func New(endpoint string, resource map[string]string, client *http.Client) *Exporter {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	e := &Exporter{endpoint: endpoint, resource: resource, client: client, ch: make(chan Record, 4096)}
	e.wg.Add(1)
	go e.loop()
	return e
}

// SetResource replaces resource attributes (e.g. once a device ID is known).
func (e *Exporter) SetResource(k, v string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.resource == nil {
		e.resource = map[string]string{}
	}
	e.resource[k] = v
}

func (e *Exporter) SetEndpoint(u string) {
	e.mu.Lock()
	e.endpoint = u
	e.mu.Unlock()
}

func (e *Exporter) Emit(name string, attrs map[string]any) {
	e.EmitSev("INFO", name, attrs)
}

func (e *Exporter) EmitSev(sev, name string, attrs map[string]any) {
	r := Record{Time: time.Now(), Severity: sev, Name: name, Attrs: attrs}
	select {
	case e.ch <- r:
	default:
		log.Printf("otlp: queue full, dropping %s", name)
	}
}

func (e *Exporter) loop() {
	defer e.wg.Done()
	var batch []Record
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := e.send(batch); err != nil {
			log.Printf("otlp: send %d records: %v", len(batch), err)
			// keep a bounded backlog for retry (disk queue lives in the collector)
			if len(batch) > 2000 {
				batch = batch[len(batch)-2000:]
			}
			return
		}
		batch = batch[:0]
	}
	for {
		select {
		case r := <-e.ch:
			batch = append(batch, r)
			if len(batch) >= 200 {
				flush()
			}
		case <-tick.C:
			flush()
		}
	}
}

func kv(k string, v any) map[string]any {
	return map[string]any{"key": k, "value": AnyValue(v)}
}

// AnyValue converts a Go value to an OTLP JSON AnyValue.
func AnyValue(v any) map[string]any {
	switch x := v.(type) {
	case string:
		return map[string]any{"stringValue": x}
	case bool:
		return map[string]any{"boolValue": x}
	case int:
		return map[string]any{"intValue": strconv.Itoa(x)}
	case int64:
		return map[string]any{"intValue": strconv.FormatInt(x, 10)}
	case float64:
		return map[string]any{"doubleValue": x}
	case []string:
		vals := make([]any, 0, len(x))
		for _, s := range x {
			vals = append(vals, map[string]any{"stringValue": s})
		}
		return map[string]any{"arrayValue": map[string]any{"values": vals}}
	case nil:
		return map[string]any{"stringValue": ""}
	default:
		b, _ := json.Marshal(x)
		return map[string]any{"stringValue": string(b)}
	}
}

func (e *Exporter) Payload(batch []Record) []byte {
	e.mu.RLock()
	res := make([]any, 0, len(e.resource))
	for k, v := range e.resource {
		res = append(res, kv(k, v))
	}
	e.mu.RUnlock()
	recs := make([]any, 0, len(batch))
	for _, r := range batch {
		attrs := []any{kv("event.name", r.Name)}
		for k, v := range r.Attrs {
			attrs = append(attrs, kv(k, v))
		}
		rec := map[string]any{
			"timeUnixNano":         strconv.FormatInt(r.Time.UnixNano(), 10),
			"observedTimeUnixNano": strconv.FormatInt(r.Time.UnixNano(), 10),
			"severityText":         r.Severity,
			"severityNumber":       sevNum(r.Severity),
			"body":                 map[string]any{"stringValue": r.Name},
			"attributes":           attrs,
		}
		if r.TraceID != "" {
			rec["traceId"] = r.TraceID
		}
		recs = append(recs, rec)
	}
	body := map[string]any{"resourceLogs": []any{map[string]any{
		"resource":  map[string]any{"attributes": res},
		"scopeLogs": []any{map[string]any{"scope": map[string]any{"name": "agentbox"}, "logRecords": recs}},
	}}}
	b, _ := json.Marshal(body)
	return b
}

func sevNum(s string) int {
	switch s {
	case "DEBUG":
		return 5
	case "WARN":
		return 13
	case "ERROR":
		return 17
	case "FATAL":
		return 21
	}
	return 9
}

func (e *Exporter) send(batch []Record) error {
	e.mu.RLock()
	ep := e.endpoint
	e.mu.RUnlock()
	if ep == "" {
		for _, r := range batch {
			log.Printf("event %s %v", r.Name, r.Attrs)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", ep, bytes.NewReader(e.Payload(batch)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("collector returned %s", resp.Status)
	}
	return nil
}
