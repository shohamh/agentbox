// Package keymint mints the short-lived LiteLLM virtual keys the gateway attaches
// to model requests. One key belongs to one sandbox (and so to one user, one
// attested device and one policy tier), carries only that device's models, and
// expires on its own a few minutes after it was minted. Nothing but the gateway
// ever sees a key: the sandbox holds a local token that is useless anywhere else.
package keymint

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Config is the key policy. Zero values give the defaults.
type Config struct {
	LiteLLM string // base URL of the LiteLLM proxy
	Master  string // LiteLLM master (or proxy-admin) key; only the gateway holds it

	TTL         time.Duration // key lifetime (default 15m)
	RenewBefore time.Duration // mint a replacement when less than this is left (default TTL/3)
	RPM         int           // requests per minute per key (0: no limit)
	TPM         int           // tokens per minute per key (0: no limit)
	Parallel    int           // concurrent requests per key (0: no limit)

	Client *http.Client
	Now    func() time.Time
}

// Subject is who the key is for.
type Subject struct {
	User, Device, Sandbox string
	Tier                  int
	Models                []string
	// MaxBudget caps what this one key may spend (USD); 0 means no per-key cap
	// (the per-user budget is still enforced by the gateway).
	MaxBudget float64
}

func (s Subject) id() string {
	return fmt.Sprintf("%s|%s|%s|%d", s.User, s.Device, s.Sandbox, s.Tier)
}

type entry struct {
	key     string
	expires time.Time
	device  string
	sandbox string
	models  string
}

// Minter creates, caches and revokes keys.
type Minter struct {
	cfg Config
	mu  sync.Mutex
	// inflight serializes minting per subject so a burst of requests from one
	// sandbox makes one key, not one per request.
	keys map[string]entry
	// retired are keys replaced at rotation: still valid until they expire (a
	// request in flight keeps working), then deleted by Prune.
	retired []entry
}

func New(cfg Config) *Minter {
	if cfg.TTL <= 0 {
		cfg.TTL = 15 * time.Minute
	}
	if cfg.RenewBefore <= 0 || cfg.RenewBefore >= cfg.TTL {
		cfg.RenewBefore = cfg.TTL / 3
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.LiteLLM = strings.TrimRight(cfg.LiteLLM, "/")
	return &Minter{cfg: cfg, keys: map[string]entry{}}
}

// TTL reports the configured key lifetime.
func (m *Minter) TTL() time.Duration { return m.cfg.TTL }

// Key returns a valid key for the subject, minting a new one when the cached
// key has less than RenewBefore left. Keys it replaces are left to expire (a
// request already in flight keeps working); they are deleted only by Revoke.
func (m *Minter) Key(ctx context.Context, s Subject) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := s.id()
	models := strings.Join(s.Models, ",")
	if e, ok := m.keys[id]; ok && e.models == models && e.expires.Sub(m.cfg.Now()) > m.cfg.RenewBefore {
		return e.key, nil
	}
	now := m.cfg.Now()
	// Seconds, not "15m": LiteLLM parses "<n>s|m|h|d" and seconds are exact.
	secs := int(m.cfg.TTL.Seconds())
	if secs < 1 {
		secs = 1
	}
	req := map[string]any{
		"key_alias": fmt.Sprintf("abx:%s:%s:%s:t%d:%d", s.User, s.Device, s.Sandbox, s.Tier, now.Unix()),
		"user_id":   s.User,
		"models":    s.Models,
		"duration":  fmt.Sprintf("%ds", secs),
		"metadata": map[string]any{
			"agentbox.user": s.User, "agentbox.device": s.Device, "agentbox.sandbox": s.Sandbox, "agentbox.tier": s.Tier,
			"agentbox.minted_by": "gatekeeper",
		},
	}
	if m.cfg.RPM > 0 {
		req["rpm_limit"] = m.cfg.RPM
	}
	if m.cfg.TPM > 0 {
		req["tpm_limit"] = m.cfg.TPM
	}
	if m.cfg.Parallel > 0 {
		req["max_parallel_requests"] = m.cfg.Parallel
	}
	if s.MaxBudget > 0 {
		req["max_budget"] = s.MaxBudget
	}
	var out struct {
		Key     string `json:"key"`
		Expires string `json:"expires"`
	}
	if err := m.call(ctx, "/key/generate", req, &out); err != nil {
		// Inside the renew window the current key is still valid: keep using it
		// rather than failing requests because a renewal could not be made.
		if e, ok := m.keys[id]; ok && e.models == models && e.expires.After(now) {
			return e.key, nil
		}
		return "", err
	}
	if out.Key == "" {
		return "", fmt.Errorf("litellm returned no key")
	}
	exp := now.Add(m.cfg.TTL)
	if t, err := time.Parse(time.RFC3339, out.Expires); err == nil && t.Before(exp) {
		exp = t // honour what LiteLLM actually granted
	}
	if old, ok := m.keys[id]; ok {
		m.retired = append(m.retired, old)
	}
	m.keys[id] = entry{key: out.Key, expires: exp, device: s.Device, sandbox: s.Sandbox, models: models}
	return out.Key, nil
}

// RevokeDevice deletes every key minted for a device (attestation lost,
// device revoked, user suspended) and forgets them.
func (m *Minter) RevokeDevice(device string) int {
	return m.revoke(func(e entry) bool { return e.device == device })
}

// RevokeSandbox deletes the keys of one sandbox (it was stopped or destroyed).
func (m *Minter) RevokeSandbox(sandbox string) int {
	return m.revoke(func(e entry) bool { return e.sandbox == sandbox })
}

func (m *Minter) revoke(match func(entry) bool) int {
	m.mu.Lock()
	var del []string
	for id, e := range m.keys {
		if match(e) {
			del = append(del, e.key)
			delete(m.keys, id)
		}
	}
	m.mu.Unlock()
	if len(del) == 0 {
		return 0
	}
	if err := m.call(context.Background(), "/key/delete", map[string]any{"keys": del}, nil); err != nil {
		// The keys are forgotten here and expire on their own within TTL.
		return 0
	}
	return len(del)
}

// Prune deletes keys that expired more than a minute ago from LiteLLM and
// forgets them, so neither the cache nor LiteLLM's key table grows with every
// rotation of every sandbox that ever ran. It returns how many it deleted.
func (m *Minter) Prune() int {
	m.mu.Lock()
	cutoff := m.cfg.Now().Add(-time.Minute)
	var del []string
	for id, e := range m.keys {
		if e.expires.Before(cutoff) {
			del = append(del, e.key)
			delete(m.keys, id)
		}
	}
	kept := m.retired[:0]
	for _, e := range m.retired {
		if e.expires.Before(cutoff) {
			del = append(del, e.key)
		} else {
			kept = append(kept, e)
		}
	}
	m.retired = kept
	m.mu.Unlock()
	if len(del) == 0 {
		return 0
	}
	if err := m.call(context.Background(), "/key/delete", map[string]any{"keys": del}, nil); err != nil {
		return 0 // expired keys are harmless; the next prune does not retry these, LiteLLM keeps them until deleted
	}
	return len(del)
}

// Cached reports how many keys are currently cached.
func (m *Minter) Cached() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.keys)
}

func (m *Minter) call(ctx context.Context, path string, body any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", m.cfg.LiteLLM+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.cfg.Master)
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.cfg.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("litellm %s: %s: %s", path, resp.Status, strings.TrimSpace(string(data)))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}
