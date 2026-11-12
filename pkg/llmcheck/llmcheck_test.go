package llmcheck

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeProxy is a small LiteLLM: virtual keys with an expiry, model scoping and
// deletion, with switches to break each property the check must catch.
type fakeProxy struct {
	mu           sync.Mutex
	keys         map[string]key
	n            int
	ignoreExpiry bool // keeps honouring an expired key
	ignoreDelete bool // keeps honouring a deleted key
	noMessages   bool // no /v1/messages route
	noModels     bool
}

type key struct {
	models  []string
	expires time.Time
	deleted bool
}

func (f *fakeProxy) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		master := tok == "sk-master"
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/health/liveliness":
			w.Write([]byte(`"I'm alive!"`))
			return
		case "/key/generate":
			if !master {
				http.Error(w, "no", 401)
				return
			}
			f.n++
			k := "sk-" + string(rune('a'+f.n))
			secs := 3600
			if d, _ := body["duration"].(string); strings.HasSuffix(d, "s") {
				secs = 0
				for _, c := range strings.TrimSuffix(d, "s") {
					secs = secs*10 + int(c-'0')
				}
			}
			var ms []string
			if list, ok := body["models"].([]any); ok {
				for _, m := range list {
					ms = append(ms, m.(string))
				}
			}
			exp := time.Now().Add(time.Duration(secs) * time.Second)
			f.keys[k] = key{models: ms, expires: exp}
			json.NewEncoder(w).Encode(map[string]any{"key": k, "expires": exp.UTC().Format("2006-01-02T15:04:05.999999")})
			return
		case "/key/delete":
			for _, k := range body["keys"].([]any) {
				e := f.keys[k.(string)]
				e.deleted = true
				f.keys[k.(string)] = e
			}
			w.Write([]byte(`{}`))
			return
		case "/get/config/callbacks":
			w.Write([]byte(`{"callbacks":["abx_callback.handler"]}`))
			return
		}
		// everything else needs a valid key
		if !master {
			k, ok := f.keys[tok]
			if !ok || (k.deleted && !f.ignoreDelete) || (time.Now().After(k.expires) && !f.ignoreExpiry) {
				http.Error(w, "invalid key", 401)
				return
			}
			if m, _ := body["model"].(string); m != "" && len(k.models) > 0 {
				allowed := false
				for _, x := range k.models {
					allowed = allowed || x == m
				}
				if !allowed {
					http.Error(w, "model not allowed", 401)
					return
				}
			}
		}
		switch r.URL.Path {
		case "/model/info", "/v1/models":
			if f.noModels {
				w.Write([]byte(`{"data":[]}`))
				return
			}
			w.Write([]byte(`{"data":[{"id":"m1"},{"id":"m2"}]}`))
		case "/v1/messages":
			if f.noMessages {
				http.NotFound(w, r)
				return
			}
			w.Write([]byte(`{"type":"message"}`))
		default:
			w.Write([]byte(`{"ok":true}`))
		}
	})
}

func run(t *testing.T, f *fakeProxy, mutate func(*Config)) map[string]Result {
	t.Helper()
	f.keys = map[string]key{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	cfg := Config{URL: srv.URL, MasterKey: "sk-master", Models: []string{"m1", "m2"}, TTL: 10 * time.Minute, Completions: true, Expiry: false}
	if mutate != nil {
		mutate(&cfg)
	}
	out := map[string]Result{}
	for _, r := range Run(context.Background(), cfg) {
		out[r.Name] = r
	}
	return out
}

func status(t *testing.T, res map[string]Result, name string) Status {
	t.Helper()
	r, ok := res[name]
	if !ok {
		t.Fatalf("no check named %q; have %v", name, res)
	}
	return r.Status
}

func TestAHealthyProxyPasses(t *testing.T) {
	res := run(t, &fakeProxy{}, func(c *Config) { c.Expiry = true })
	for name, r := range res {
		if r.Status != OK {
			t.Errorf("%s: %s %s", name, r.Status, r.Detail)
		}
	}
}

func TestWrongMasterKeyIsReported(t *testing.T) {
	res := run(t, &fakeProxy{}, func(c *Config) { c.MasterKey = "sk-wrong" })
	if status(t, res, "master key accepted") != Fail {
		t.Fatal("a rejected master key must fail")
	}
}

func TestMissingModelIsReported(t *testing.T) {
	res := run(t, &fakeProxy{}, func(c *Config) { c.Models = []string{"m1", "nope"} })
	r := res["models configured"]
	if r.Status != Fail || !strings.Contains(r.Detail, "nope") {
		t.Fatalf("missing model not reported: %+v", r)
	}
}

func TestProxyThatIgnoresExpiryFails(t *testing.T) {
	res := run(t, &fakeProxy{ignoreExpiry: true}, func(c *Config) { c.Expiry = true })
	if status(t, res, "expired keys are refused") != Fail {
		t.Fatal("a proxy that keeps honouring expired keys must fail")
	}
}

func TestProxyThatIgnoresDeletionFails(t *testing.T) {
	res := run(t, &fakeProxy{ignoreDelete: true}, nil)
	if status(t, res, "deleted keys stop working at once") != Fail {
		t.Fatal("a proxy that keeps honouring deleted keys must fail")
	}
}

func TestMissingMessagesAPIFails(t *testing.T) {
	res := run(t, &fakeProxy{noMessages: true}, nil)
	if status(t, res, "messages API (Claude Code)") != Fail {
		t.Fatal("no /v1/messages must fail: Claude Code cannot work")
	}
}

func TestProbeKeysAreDeleted(t *testing.T) {
	f := &fakeProxy{}
	run(t, f, nil)
	for k, v := range f.keys {
		if !v.deleted {
			t.Errorf("probe key %s left behind", k)
		}
	}
}

func TestUnreachableProxy(t *testing.T) {
	res := Run(context.Background(), Config{URL: "http://127.0.0.1:1", MasterKey: "x"})
	if len(res) != 1 || res[0].Status != Fail {
		t.Fatalf("got %+v", res)
	}
}

func TestParseExpiry(t *testing.T) {
	for _, v := range []string{"2026-11-12T08:30:00.123456", "2026-11-12T08:30:00", "2026-11-12T08:30:00.123+00:00", "2026-11-12T08:30:00Z"} {
		if _, ok := parseExpiry(v); !ok {
			t.Errorf("cannot parse %q", v)
		}
	}
	if _, ok := parseExpiry(""); ok {
		t.Error("empty expiry accepted")
	}
}
