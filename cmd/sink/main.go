// sink stands in for Splunk or any other OTLP backend in the test bed. It
// accepts OTLP/HTTP logs (JSON) on :4318 and Splunk HEC events on :8088 and
// counts what arrives; GET /stats on either port reports the counts, and
// POST /reset clears them.
package main

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type counters struct {
	Requests int            `json:"requests"`
	Records  int            `json:"records"`
	ByEvent  map[string]int `json:"by_event"`
	Last     []map[string]any `json:"last"`
	Protobuf int            `json:"protobuf_requests,omitempty"`
	LastAt   time.Time      `json:"last_at"`
	Token    string         `json:"last_token,omitempty"`
}

var (
	mu   sync.Mutex
	otlp = counters{ByEvent: map[string]int{}}
	hec  = counters{ByEvent: map[string]int{}}
)

func main() {
	hecToken := os.Getenv("SINK_HEC_TOKEN")
	otlpMux := http.NewServeMux()
	otlpMux.HandleFunc("POST /v1/logs", otlpLogs)
	hecMux := http.NewServeMux()
	hecMux.HandleFunc("POST /services/collector/event", hecEvents(hecToken))
	hecMux.HandleFunc("POST /services/collector", hecEvents(hecToken))
	for _, m := range []*http.ServeMux{otlpMux, hecMux} {
		m.HandleFunc("GET /stats", stats)
		m.HandleFunc("POST /reset", reset)
		m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	}
	go func() { log.Fatal(http.ListenAndServe(":8088", hecMux)) }()
	log.Println("sink: OTLP/HTTP on :4318, Splunk HEC on :8088")
	log.Fatal(http.ListenAndServe(":4318", otlpMux))
}

func body(r *http.Request) []byte {
	var rd io.Reader = r.Body
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		if gz, err := gzip.NewReader(r.Body); err == nil {
			rd = gz
		}
	}
	b, _ := io.ReadAll(io.LimitReader(rd, 32<<20))
	return b
}

func stats(w http.ResponseWriter, _ *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"otlp": otlp, "hec": hec})
}

func reset(w http.ResponseWriter, _ *http.Request) {
	mu.Lock()
	otlp, hec = counters{ByEvent: map[string]int{}}, counters{ByEvent: map[string]int{}}
	mu.Unlock()
	w.Write([]byte("ok"))
}

func (c *counters) add(rec map[string]any, event string) {
	c.Records++
	if event != "" {
		c.ByEvent[event]++
	}
	c.Last = append(c.Last, rec)
	if len(c.Last) > 200 {
		c.Last = c.Last[len(c.Last)-200:]
	}
}

// anyValue flattens an OTLP AnyValue.
func anyValue(v map[string]any) any {
	for _, k := range []string{"stringValue", "intValue", "boolValue", "doubleValue"} {
		if x, ok := v[k]; ok {
			return x
		}
	}
	if a, ok := v["arrayValue"].(map[string]any); ok {
		var out []any
		vals, _ := a["values"].([]any)
		for _, e := range vals {
			if m, ok := e.(map[string]any); ok {
				out = append(out, anyValue(m))
			}
		}
		return out
	}
	return nil
}

func attrs(l any) map[string]any {
	out := map[string]any{}
	arr, _ := l.([]any)
	for _, e := range arr {
		m, _ := e.(map[string]any)
		k, _ := m["key"].(string)
		if v, ok := m["value"].(map[string]any); ok && k != "" {
			out[k] = anyValue(v)
		}
	}
	return out
}

func otlpLogs(w http.ResponseWriter, r *http.Request) {
	b := body(r)
	mu.Lock()
	defer mu.Unlock()
	otlp.Requests++
	otlp.LastAt = time.Now()
	if !strings.Contains(r.Header.Get("Content-Type"), "json") {
		otlp.Protobuf++
		w.Header().Set("Content-Type", r.Header.Get("Content-Type"))
		return
	}
	var req struct {
		ResourceLogs []struct {
			Resource struct {
				Attributes []any `json:"attributes"`
			} `json:"resource"`
			ScopeLogs []struct {
				LogRecords []map[string]any `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
	if err := json.Unmarshal(b, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	for _, rl := range req.ResourceLogs {
		res := attrs(rl.Resource.Attributes)
		for _, sl := range rl.ScopeLogs {
			for _, rec := range sl.LogRecords {
				a := attrs(rec["attributes"])
				ev, _ := a["event.name"].(string)
				if ev == "" {
					if body, ok := rec["body"].(map[string]any); ok {
						ev, _ = body["stringValue"].(string)
					}
				}
				a["_resource"] = res
				otlp.add(a, ev)
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte("{}"))
}

// findKey returns the first value stored under key anywhere in v.
func findKey(v any, key string) (any, bool) {
	switch x := v.(type) {
	case map[string]any:
		if val, ok := x[key]; ok {
			return val, true
		}
		for _, c := range x {
			if r, ok := findKey(c, key); ok {
				return r, true
			}
		}
	case []any:
		for _, c := range x {
			if r, ok := findKey(c, key); ok {
				return r, true
			}
		}
	}
	return nil, false
}

func hecEvents(token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Splunk ")
		w.Header().Set("Content-Type", "application/json")
		if token != "" && got != token {
			w.WriteHeader(401)
			w.Write([]byte(`{"text":"Invalid token","code":4}`))
			return
		}
		dec := json.NewDecoder(strings.NewReader(string(body(r))))
		mu.Lock()
		defer mu.Unlock()
		hec.Requests++
		hec.LastAt, hec.Token = time.Now(), got
		for {
			var ev map[string]any
			if err := dec.Decode(&ev); err != nil {
				break
			}
			name := ""
			if v, ok := findKey(ev, "event.name"); ok {
				name, _ = v.(string)
			} else if e, ok := ev["event"].(map[string]any); ok {
				name, _ = e["name"].(string)
			}
			hec.add(ev, name)
		}
		w.Write([]byte(`{"text":"Success","code":0}`))
	}
}
