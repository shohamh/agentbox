// Package llmcheck verifies that a LiteLLM proxy is ready to serve as Agentbox's
// model gateway. It does what the gateway will do in production: mint short
// keys with the master key, use them for the three APIs the harnesses speak
// (chat completions, Anthropic messages, responses), and confirm that expiry,
// model scoping and deletion are enforced by LiteLLM itself. The LiteLLM
// maintainers run it (agentbox-preflight) before pointing Agentbox at a proxy,
// and after every LiteLLM upgrade.
package llmcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Status string

const (
	OK   Status = "ok"
	Warn Status = "warn"
	Fail Status = "FAIL"
	Skip Status = "skip"
)

// Result is one check.
type Result struct {
	Name   string
	Status Status
	Detail string // what was seen
	Fix    string // what to change when it is not ok
}

type Config struct {
	URL       string   // LiteLLM base URL
	MasterKey string   // the key Agentbox's gateway will hold
	Models    []string // models the groups may use (aliases as callers name them)
	TTL       time.Duration
	// Completions: send one tiny request per API (costs a few tokens).
	Completions bool
	// Expiry: mint a 3-second key and prove LiteLLM refuses it afterwards.
	Expiry bool
	Client *http.Client
}

type checker struct {
	cfg  Config
	res  []Result
	keys []string // probe keys to delete at the end
}

func (c *checker) add(name string, st Status, detail, fix string) {
	c.res = append(c.res, Result{Name: name, Status: st, Detail: detail, Fix: fix})
}

// Run performs every check and always cleans up the probe keys it made.
func Run(ctx context.Context, cfg Config) []Result {
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 60 * time.Second}
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 15 * time.Minute
	}
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	c := &checker{cfg: cfg}
	defer c.cleanup(ctx)

	if !c.reachable(ctx) {
		return c.res
	}
	if !c.masterKey(ctx) {
		return c.res
	}
	c.modelsExist(ctx)
	probe := c.mintProbe(ctx)
	if probe == "" {
		return c.res
	}
	c.keyScoping(ctx, probe)
	if cfg.Completions && len(cfg.Models) > 0 {
		c.apis(ctx, probe, cfg.Models[0])
	} else {
		c.add("chat, messages and responses APIs", Skip, "completions disabled", "")
	}
	if cfg.Expiry {
		c.expiry(ctx)
	} else {
		c.add("expired keys are refused", Skip, "expiry check disabled", "")
	}
	c.deletion(ctx)
	c.callback(ctx)
	return c.res
}

// Failed reports whether any check failed.
func Failed(rs []Result) bool {
	for _, r := range rs {
		if r.Status == Fail {
			return true
		}
	}
	return false
}

func (c *checker) do(ctx context.Context, method, path string, key string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.URL+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.cfg.Client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
}

func short(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 220 {
		s = s[:220] + "..."
	}
	return s
}

func (c *checker) reachable(ctx context.Context) bool {
	code, b, err := c.do(ctx, "GET", "/health/liveliness", "", nil)
	if err != nil {
		c.add("LiteLLM reachable", Fail, err.Error(), "The gateway (gatekeeper) must reach LiteLLM_URL over the network, and LiteLLM must listen on it. Check the URL, DNS, firewall and TLS trust.")
		return false
	}
	if code != 200 {
		c.add("LiteLLM reachable", Fail, fmt.Sprintf("GET /health/liveliness: %d %s", code, short(b)), "LiteLLM answered but is not healthy: look at its logs.")
		return false
	}
	c.add("LiteLLM reachable", OK, c.cfg.URL, "")
	return true
}

func (c *checker) masterKey(ctx context.Context) bool {
	code, b, err := c.do(ctx, "GET", "/model/info", c.cfg.MasterKey, nil)
	if err != nil {
		c.add("master key accepted", Fail, err.Error(), "")
		return false
	}
	switch {
	case code == 401 || code == 403:
		c.add("master key accepted", Fail, fmt.Sprintf("/model/info: %d %s", code, short(b)), "Give the gateway the proxy's master key (general_settings.master_key) or the key of a proxy_admin user: it must be able to create and delete keys.")
		return false
	case code >= 300:
		c.add("master key accepted", Fail, fmt.Sprintf("/model/info: %d %s", code, short(b)), "LiteLLM needs a database (general_settings.database_url) to manage virtual keys.")
		return false
	}
	c.add("master key accepted", OK, "", "")
	return true
}

func (c *checker) modelsExist(ctx context.Context) {
	code, b, err := c.do(ctx, "GET", "/v1/models", c.cfg.MasterKey, nil)
	if err != nil || code != 200 {
		c.add("models configured", Fail, fmt.Sprintf("/v1/models: %d %v %s", code, err, short(b)), "")
		return
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	json.Unmarshal(b, &out)
	have := map[string]bool{}
	for _, m := range out.Data {
		have[m.ID] = true
	}
	var missing []string
	for _, m := range c.cfg.Models {
		if !have[m] {
			missing = append(missing, m)
		}
	}
	if len(c.cfg.Models) == 0 {
		c.add("models configured", Warn, fmt.Sprintf("%d models in LiteLLM; none named to check", len(have)), "Pass the models your groups may use (--models a,b,c).")
		return
	}
	if len(missing) > 0 {
		c.add("models configured", Fail, "missing in LiteLLM: "+strings.Join(missing, ", "), "Add each name as a model_name in LiteLLM's model_list (an alias is fine: Claude Code and Codex only ever send the names Agentbox puts in the sandbox environment).")
		return
	}
	c.add("models configured", OK, strings.Join(c.cfg.Models, ", "), "")
}

type gen struct {
	Key     string `json:"key"`
	Expires string `json:"expires"`
}

func (c *checker) generate(ctx context.Context, body map[string]any) (gen, int, []byte, error) {
	code, b, err := c.do(ctx, "POST", "/key/generate", c.cfg.MasterKey, body)
	var g gen
	if err == nil && code < 300 {
		json.Unmarshal(b, &g)
		if g.Key != "" {
			c.keys = append(c.keys, g.Key)
		}
	}
	return g, code, b, err
}

func (c *checker) mintProbe(ctx context.Context) string {
	models := c.cfg.Models
	if len(models) == 0 {
		models = []string{}
	}
	secs := int(c.cfg.TTL.Seconds())
	g, code, b, err := c.generate(ctx, map[string]any{
		"key_alias": fmt.Sprintf("abx-preflight:%d", time.Now().Unix()),
		"user_id":   "agentbox-preflight",
		"models":    models,
		"duration":  fmt.Sprintf("%ds", secs),
		"metadata":  map[string]any{"agentbox.minted_by": "preflight"},
	})
	if err != nil || code >= 300 || g.Key == "" {
		c.add("short-lived key can be minted", Fail, fmt.Sprintf("/key/generate: %d %v %s", code, err, short(b)), "The master key must be allowed to call /key/generate, and LiteLLM needs its database.")
		return ""
	}
	exp, ok := parseExpiry(g.Expires)
	if !ok {
		c.add("short-lived key can be minted", Fail, "LiteLLM returned no usable expiry: "+g.Expires, "The key must carry an expiry (the `duration` field of /key/generate); upgrade LiteLLM if it ignores it.")
		return ""
	}
	left := time.Until(exp)
	if left > c.cfg.TTL+time.Minute || left < c.cfg.TTL-2*time.Minute {
		c.add("short-lived key can be minted", Fail, fmt.Sprintf("asked for %s, got a key valid for about %s", c.cfg.TTL, left.Round(time.Second)), "LiteLLM did not honour `duration`: check the proxy version.")
		return ""
	}
	c.add("short-lived key can be minted", OK, fmt.Sprintf("valid for %s", c.cfg.TTL), "")
	return g.Key
}

func (c *checker) keyScoping(ctx context.Context, probe string) {
	if len(c.cfg.Models) < 2 {
		c.add("a key only works for its models", Skip, "needs two models to prove", "")
		return
	}
	g, code, b, err := c.generate(ctx, map[string]any{
		"key_alias": fmt.Sprintf("abx-preflight-scope:%d", time.Now().Unix()), "user_id": "agentbox-preflight",
		"models": c.cfg.Models[:1], "duration": "120s",
	})
	if err != nil || code >= 300 {
		c.add("a key only works for its models", Fail, fmt.Sprintf("%d %v %s", code, err, short(b)), "")
		return
	}
	other := c.cfg.Models[1]
	code, b, _ = c.do(ctx, "POST", "/v1/chat/completions", g.Key, map[string]any{"model": other, "max_tokens": 1, "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if code == 200 {
		c.add("a key only works for its models", Fail, "a key limited to "+c.cfg.Models[0]+" could call "+other, "Set `models` on the key (the gateway does) and do not give LiteLLM teams or wildcard access that overrides it.")
		return
	}
	c.add("a key only works for its models", OK, fmt.Sprintf("%s refused with %d", other, code), "")
}

func (c *checker) apis(ctx context.Context, key, model string) {
	msg := []any{map[string]any{"role": "user", "content": "Reply with one word."}}
	tests := []struct {
		name, path string
		body       map[string]any
		need       Status // failure severity
		fix        string
	}{
		{"chat completions (opencode, omp, most harnesses)", "/v1/chat/completions", map[string]any{"model": model, "max_tokens": 5, "messages": msg}, Fail,
			"Required by every harness except Claude Code and Codex."},
		{"messages API (Claude Code)", "/v1/messages", map[string]any{"model": model, "max_tokens": 5, "messages": msg}, Fail,
			"Claude Code speaks Anthropic's Messages API. LiteLLM serves /v1/messages for every model; if this fails, check the model's provider settings and upgrade LiteLLM."},
		{"responses API (Codex)", "/v1/responses", map[string]any{"model": model, "max_output_tokens": 16, "input": "Reply with one word."}, Warn,
			"Codex needs the Responses API. Without it only the Codex image fails; the other harnesses are unaffected."},
	}
	for _, t := range tests {
		code, b, err := c.do(ctx, "POST", t.path, key, t.body)
		switch {
		case err != nil:
			c.add(t.name, t.need, err.Error(), t.fix)
		case code != 200:
			c.add(t.name, t.need, fmt.Sprintf("%s %s: %d %s", model, t.path, code, short(b)), t.fix)
		default:
			c.add(t.name, OK, model, "")
		}
	}
	// A streamed reply must arrive as an event stream, not buffered JSON.
	code, _, err := c.do(ctx, "POST", "/v1/chat/completions", key, map[string]any{"model": model, "max_tokens": 5, "stream": true, "messages": msg})
	if err != nil || code != 200 {
		c.add("streaming", Fail, fmt.Sprintf("%d %v", code, err), "Every harness streams. Check proxies in front of LiteLLM do not buffer or cut event streams.")
	} else {
		c.add("streaming", OK, "", "")
	}
}

func (c *checker) expiry(ctx context.Context) {
	g, code, b, err := c.generate(ctx, map[string]any{
		"key_alias": fmt.Sprintf("abx-preflight-exp:%d", time.Now().Unix()), "user_id": "agentbox-preflight", "duration": "3s",
	})
	if err != nil || code >= 300 {
		c.add("expired keys are refused", Fail, fmt.Sprintf("%d %v %s", code, err, short(b)), "")
		return
	}
	code, _, _ = c.do(ctx, "GET", "/v1/models", g.Key, nil)
	if code != 200 {
		c.add("expired keys are refused", Fail, fmt.Sprintf("a fresh 3-second key was refused with %d", code), "")
		return
	}
	select {
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
		return
	}
	code, b, _ = c.do(ctx, "GET", "/v1/models", g.Key, nil)
	if code == 200 {
		c.add("expired keys are refused", Fail, "a key kept working after its expiry", "LiteLLM must enforce `duration`: upgrade it, and make sure no caching layer in front of it serves old authorizations.")
		return
	}
	c.add("expired keys are refused", OK, fmt.Sprintf("refused with %d after 5s", code), "")
}

func (c *checker) deletion(ctx context.Context) {
	g, code, b, err := c.generate(ctx, map[string]any{
		"key_alias": fmt.Sprintf("abx-preflight-del:%d", time.Now().Unix()), "user_id": "agentbox-preflight", "duration": "120s",
	})
	if err != nil || code >= 300 {
		c.add("deleted keys stop working at once", Fail, fmt.Sprintf("%d %v %s", code, err, short(b)), "")
		return
	}
	code, _, _ = c.do(ctx, "POST", "/key/delete", c.cfg.MasterKey, map[string]any{"keys": []string{g.Key}})
	if code >= 300 {
		c.add("deleted keys stop working at once", Fail, fmt.Sprintf("/key/delete: %d", code), "")
		return
	}
	code, _, _ = c.do(ctx, "GET", "/v1/models", g.Key, nil)
	if code == 200 {
		c.add("deleted keys stop working at once", Fail, "a deleted key still works", "LiteLLM caches keys in memory for a short time; the gateway relies on deletion when a device loses attestation. Lower `user_api_key_cache_ttl` or use a single worker.")
		return
	}
	c.add("deleted keys stop working at once", OK, fmt.Sprintf("refused with %d", code), "")
}

func (c *checker) callback(ctx context.Context) {
	code, b, err := c.do(ctx, "GET", "/get/config/callbacks", c.cfg.MasterKey, nil)
	if err != nil || code != 200 {
		c.add("Agentbox telemetry callback installed", Warn, fmt.Sprintf("could not read callbacks (%d)", code), "Install configs/control/litellm/abx_callback.py and set litellm_settings.callbacks: abx_callback.handler. Without it the console has no model-call events.")
		return
	}
	if strings.Contains(string(b), "abx_callback") {
		c.add("Agentbox telemetry callback installed", OK, "", "")
		return
	}
	c.add("Agentbox telemetry callback installed", Warn, "abx_callback not in the active callbacks", "Install configs/control/litellm/abx_callback.py on the proxy and set litellm_settings.callbacks: abx_callback.handler. Without it the console has no model-call events or tool-call correlation.")
}

func (c *checker) cleanup(ctx context.Context) {
	if len(c.keys) == 0 {
		return
	}
	c.do(context.WithoutCancel(ctx), "POST", "/key/delete", c.cfg.MasterKey, map[string]any{"keys": c.keys})
}

// parseExpiry reads LiteLLM's "expires": ISO 8601, with or without a zone
// (without one, LiteLLM means UTC).
func parseExpiry(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, v, time.UTC); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
