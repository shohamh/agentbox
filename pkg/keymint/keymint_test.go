package keymint

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeLiteLLM records what the gateway asks of LiteLLM.
type fakeLiteLLM struct {
	mu       sync.Mutex
	srv      *httptest.Server
	n        int
	gen      []map[string]any
	deleted  []string
	failGen  bool
	lastAuth string
}

func newFake(t *testing.T) *fakeLiteLLM {
	f := &fakeLiteLLM{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.lastAuth = r.Header.Get("Authorization")
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/key/generate":
			if f.failGen {
				http.Error(w, "down", 503)
				return
			}
			f.n++
			f.gen = append(f.gen, body)
			json.NewEncoder(w).Encode(map[string]any{"key": fmt.Sprintf("sk-key-%d", f.n)})
		case "/key/delete":
			for _, k := range body["keys"].([]any) {
				f.deleted = append(f.deleted, k.(string))
			}
			w.Write([]byte(`{"deleted_keys":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func subj(sandbox string) Subject {
	return Subject{User: "alice", Device: "pc-1", Sandbox: sandbox, Tier: 1, Models: []string{"deepseek-v3", "glm-4"}}
}

func TestKeyIsShortLivedAndBoundToTheSandbox(t *testing.T) {
	f := newFake(t)
	m := New(Config{LiteLLM: f.srv.URL, Master: "sk-master", TTL: 10 * time.Minute, RPM: 60, TPM: 90000, Parallel: 4})
	k, err := m.Key(context.Background(), Subject{User: "alice", Device: "pc-1", Sandbox: "sb-1", Tier: 1, Models: []string{"deepseek-v3"}, MaxBudget: 2.5})
	if err != nil || k == "" {
		t.Fatalf("Key: %q %v", k, err)
	}
	g := f.gen[0]
	if g["duration"] != "600s" {
		t.Errorf("duration = %v, want 600s", g["duration"])
	}
	if g["user_id"] != "alice" || fmt.Sprint(g["models"]) != "[deepseek-v3]" {
		t.Errorf("user or models wrong: %v", g)
	}
	md := g["metadata"].(map[string]any)
	if md["agentbox.sandbox"] != "sb-1" || md["agentbox.device"] != "pc-1" {
		t.Errorf("key is not bound to the sandbox and device: %v", md)
	}
	if g["rpm_limit"].(float64) != 60 || g["tpm_limit"].(float64) != 90000 || g["max_parallel_requests"].(float64) != 4 || g["max_budget"].(float64) != 2.5 {
		t.Errorf("limits missing: %v", g)
	}
	if f.lastAuth != "Bearer sk-master" {
		t.Errorf("minted with %q, want the master key", f.lastAuth)
	}
}

func TestKeyIsReusedThenRenewedBeforeItExpires(t *testing.T) {
	f := newFake(t)
	c := &clock{time.Now()}
	m := New(Config{LiteLLM: f.srv.URL, Master: "m", TTL: 15 * time.Minute, RenewBefore: 5 * time.Minute, Now: c.now})
	k1, _ := m.Key(context.Background(), subj("sb-1"))
	c.t = c.t.Add(9 * time.Minute) // 6 minutes left: still fine
	k2, _ := m.Key(context.Background(), subj("sb-1"))
	if k1 != k2 || f.n != 1 {
		t.Fatalf("key minted again too early: %s %s (%d mints)", k1, k2, f.n)
	}
	c.t = c.t.Add(2 * time.Minute) // 4 minutes left: renew
	k3, _ := m.Key(context.Background(), subj("sb-1"))
	if k3 == k1 || f.n != 2 {
		t.Fatalf("key not renewed inside the renew window: %s (%d mints)", k3, f.n)
	}
	if len(f.deleted) != 0 {
		t.Errorf("the old key was deleted at rotation (in-flight requests would fail): %v", f.deleted)
	}
}

func TestEachSandboxGetsItsOwnKey(t *testing.T) {
	f := newFake(t)
	m := New(Config{LiteLLM: f.srv.URL, Master: "m"})
	a, _ := m.Key(context.Background(), subj("sb-1"))
	b, _ := m.Key(context.Background(), subj("sb-2"))
	if a == b {
		t.Fatal("two sandboxes share one key")
	}
}

func TestPolicyChangeMintsANewKey(t *testing.T) {
	f := newFake(t)
	m := New(Config{LiteLLM: f.srv.URL, Master: "m"})
	s := subj("sb-1")
	a, _ := m.Key(context.Background(), s)
	s.Models = []string{"deepseek-v3"} // admin removed a model
	b, _ := m.Key(context.Background(), s)
	if a == b {
		t.Fatal("a key minted for the old model list survived a policy change")
	}
}

func TestRevokeDeviceDeletesItsKeysOnly(t *testing.T) {
	f := newFake(t)
	m := New(Config{LiteLLM: f.srv.URL, Master: "m"})
	a, _ := m.Key(context.Background(), subj("sb-1"))
	s2 := subj("sb-9")
	s2.Device = "pc-2"
	b, _ := m.Key(context.Background(), s2)
	if n := m.RevokeDevice("pc-1"); n != 1 {
		t.Fatalf("revoked %d keys, want 1", n)
	}
	if len(f.deleted) != 1 || f.deleted[0] != a {
		t.Fatalf("deleted %v, want only %s (not %s)", f.deleted, a, b)
	}
	if m.Cached() != 1 {
		t.Errorf("cache holds %d keys, want 1", m.Cached())
	}
}

func TestRevokeSandbox(t *testing.T) {
	f := newFake(t)
	m := New(Config{LiteLLM: f.srv.URL, Master: "m"})
	m.Key(context.Background(), subj("sb-1"))
	m.Key(context.Background(), subj("sb-2"))
	if n := m.RevokeSandbox("sb-1"); n != 1 || m.Cached() != 1 {
		t.Fatalf("revoked %d, cached %d", n, m.Cached())
	}
}

func TestPruneDeletesExpiredAndRetiredKeysButNotLiveOnes(t *testing.T) {
	f := newFake(t)
	c := &clock{time.Now()}
	m := New(Config{LiteLLM: f.srv.URL, Master: "m", TTL: 10 * time.Minute, RenewBefore: 4 * time.Minute, Now: c.now})
	k1, _ := m.Key(context.Background(), subj("sb-1"))
	c.t = c.t.Add(7 * time.Minute) // inside the renew window
	k2, _ := m.Key(context.Background(), subj("sb-1"))
	if k1 == k2 {
		t.Fatal("no rotation")
	}
	if n := m.Prune(); n != 0 || len(f.deleted) != 0 {
		t.Fatalf("pruned %d keys that are still valid: %v", n, f.deleted)
	}
	c.t = c.t.Add(4 * time.Minute) // k1 expired 1 minute ago... plus grace
	if n := m.Prune(); n != 0 {
		t.Fatalf("pruned inside the one-minute grace: %d", n)
	}
	c.t = c.t.Add(time.Minute) // k1 is now 2 minutes past expiry; k2 still has 4 minutes
	if n := m.Prune(); n != 1 || len(f.deleted) != 1 || f.deleted[0] != k1 {
		t.Fatalf("pruned %d, deleted %v; want only the retired key %s", n, f.deleted, k1)
	}
	c.t = c.t.Add(10 * time.Minute) // k2 expired too
	if n := m.Prune(); n != 1 || m.Cached() != 0 {
		t.Fatalf("pruned %d, cached %d; want the last expired key gone", n, m.Cached())
	}
}

func TestLiteLLMDownDeniesInsteadOfHandingOutAStaleKey(t *testing.T) {
	f := newFake(t)
	c := &clock{time.Now()}
	m := New(Config{LiteLLM: f.srv.URL, Master: "m", TTL: 3 * time.Minute, RenewBefore: time.Minute, Now: c.now})
	m.Key(context.Background(), subj("sb-1"))
	c.t = c.t.Add(3*time.Minute + time.Second) // expired
	f.failGen = true
	if k, err := m.Key(context.Background(), subj("sb-1")); err == nil {
		t.Fatalf("got key %q for an expired entry while LiteLLM is down; want an error", k)
	}
}

func TestStillValidKeyIsKeptWhenRenewalFails(t *testing.T) {
	f := newFake(t)
	c := &clock{time.Now()}
	m := New(Config{LiteLLM: f.srv.URL, Master: "m", TTL: 15 * time.Minute, RenewBefore: 5 * time.Minute, Now: c.now})
	k1, _ := m.Key(context.Background(), subj("sb-1"))
	c.t = c.t.Add(12 * time.Minute) // in the renew window, 3 minutes left
	f.failGen = true
	k2, err := m.Key(context.Background(), subj("sb-1"))
	if err != nil || k2 != k1 {
		t.Fatalf("got %q %v, want the still-valid key %q", k2, err, k1)
	}
}

func TestDefaults(t *testing.T) {
	m := New(Config{LiteLLM: "http://x/"})
	if m.TTL() != 15*time.Minute || m.cfg.RenewBefore != 5*time.Minute || m.cfg.LiteLLM != "http://x" {
		t.Fatalf("defaults: ttl %v renew %v url %q", m.TTL(), m.cfg.RenewBefore, m.cfg.LiteLLM)
	}
}
