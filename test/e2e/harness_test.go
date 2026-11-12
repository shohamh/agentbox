//go:build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"agentbox/pkg/llmcheck"
)

// Harnesses and short-lived model keys.
//
//   E2E-16  Claude Code runs a real task through the whole stack
//   E2E-17  omp (Oh My Pi) runs a real task through the whole stack
//   E2E-18  model keys are short-lived, per sandbox, and the sandbox never holds one
//   E2E-19  a device that loses trust loses its keys at once and gets no new ones
//   E2E-20  the company LiteLLM passes the readiness check (what its maintainers run)

// modelsDenied counts gateway denials stamped with this sandbox since t0: a
// harness that asks for a model name the policy does not list shows up here.
func modelsDenied(t *testing.T, sandbox string, since time.Time) []string {
	rows := events(t, since, evExpr+" = 'gateway.denied' AND "+sbExpr+" = {sb:String}", map[string]string{"sb": sandbox})
	var out []string
	for _, r := range rows {
		out = append(out, fmt.Sprint(sub(r, "attrs")["reason"], ": ", sub(r, "attrs")["message"]))
	}
	return out
}

// requestedModels lists the model names the harness asked LiteLLM for.
func requestedModels(t *testing.T, sandbox string, since time.Time) map[string]bool {
	rows := events(t, since, evExpr+" = 'model.call' AND "+sbExpr+" = {sb:String}", map[string]string{"sb": sandbox})
	out := map[string]bool{}
	for _, r := range rows {
		if m := str(sub(r, "attrs")["gen_ai.request.model"]); m != "" {
			out[m] = true
		}
	}
	return out
}

func harnessTask(t *testing.T, name, image string) {
	s, t0 := agentTask(t, name, image, image)
	// Every model name the harness used must be one the policy allows: the
	// sandbox environment names the default and role models (Claude Code's
	// haiku and sub-agent models, omp's provider models) from the policy.
	allowed := map[string]bool{}
	for _, m := range strings.Fields(strings.ReplaceAll(sbRun(t, s, 20*time.Second, `echo "$AGENTBOX_MODELS"`).Out, ",", " ")) {
		allowed[m] = true
	}
	if len(allowed) == 0 {
		t.Fatalf("the sandbox does not know its allowed models (AGENTBOX_MODELS is empty)")
	}
	for m := range requestedModels(t, s.ID, t0) {
		if !allowed[m] {
			t.Errorf("%s asked for model %q, which the policy does not allow (%v)", image, m, allowed)
		}
	}
	if d := modelsDenied(t, s.ID, t0); len(d) > 0 {
		t.Errorf("the gateway denied %s requests during a normal task: %v", image, d)
	}
	// the harness holds only the sandbox-local token, never a LiteLLM key
	if tok := sandboxToken(t, s); !strings.HasPrefix(tok, "abx-sbx-") {
		t.Errorf("the sandbox token does not look like a local token: %.12s...", tok)
	}
}

func TestE2E_16_ClaudeCodeTask(t *testing.T) {
	harnessTask(t, "e2e-claude", "claude-code")
	// Interactive start: no first-run questions (trust this folder, use this API
	// key, auto mode) may stand between a developer and the prompt.
	s := findSandbox(t, pc1, "e2e-claude")
	if s == nil {
		t.Fatal("sandbox e2e-claude not found")
	}
	r := sbRun(t, *s, 90*time.Second, `cd /workspace; (sleep 14; printf '\r') | timeout 24 script -qfec claude /dev/null 2>&1 | sed 's/\x1b\[[0-9;?]*[a-zA-Z]//g' | tr -d ' \n'`)
	screen := r.Out
	if !strings.Contains(screen, "bypasspermissionson") {
		t.Errorf("Claude Code did not reach its prompt in bypass-permissions mode: %.600s", screen)
	}
	for _, q := range []string{"customAPIkey", "Quicksafetycheck", "defaultpermissionmode", "Doyouwanttousethis"} {
		if strings.Contains(screen, q) {
			t.Errorf("Claude Code stopped to ask a first-run question (%s): %.600s", q, screen)
		}
	}
}
func TestE2E_17_OmpTask(t *testing.T)        { harnessTask(t, "e2e-omp", "omp") }

// ---------- E2E-18: short-lived keys ----------

type keyRow struct {
	alias, user, device, sandbox string
	secondsLeft                  float64
	models                       string
	maxBudget                    string
}

// keysOf reads the LiteLLM keys minted for a sandbox straight from LiteLLM's database.
func keysOf(t *testing.T, sandbox string) []keyRow {
	rows := psql(t, "litellm", fmt.Sprintf(`SELECT key_alias, user_id, COALESCE(metadata->>'agentbox.device',''), COALESCE(metadata->>'agentbox.sandbox',''),
		COALESCE(EXTRACT(EPOCH FROM (expires - now())), 1e9)::float8, array_to_string(models, ','), COALESCE(max_budget::text,'')
		FROM "LiteLLM_VerificationToken" WHERE metadata->>'agentbox.sandbox' = '%s' ORDER BY created_at`, strings.ReplaceAll(sandbox, "'", "")))
	var out []keyRow
	for _, r := range rows {
		left, _ := strconv.ParseFloat(r[4], 64)
		out = append(out, keyRow{r[0], r[1], r[2], r[3], left, r[5], r[6]})
	}
	return out
}

func gatekeeperTTL(t *testing.T) time.Duration {
	v := dockerEnv(t, "gatekeeper")["KEY_TTL"]
	if v == "" {
		return 15 * time.Minute
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		t.Fatalf("gatekeeper KEY_TTL=%q: %v", v, err)
	}
	return d
}

func TestE2E_18_ShortLivedPerSandboxKeys(t *testing.T) {
	ensureEnrolled(t, pc1, user1)
	s := ensureSandbox(t, pc1, "e2e-codex", "codex")
	ttl := gatekeeperTTL(t)
	if c := modelCall(t, s, "deepseek-v3"); c.Code != 200 {
		t.Fatalf("model call: %d %s", c.Code, c.Body)
	}
	ks := keysOf(t, s.ID)
	if len(ks) == 0 {
		t.Fatalf("no LiteLLM key was minted for sandbox %s: the gateway must mint per sandbox", s.ID)
	}
	k := ks[len(ks)-1]
	t.Logf("key %s: %.0fs left (ttl %s), models %s, per-key budget %s", k.alias, k.secondsLeft, ttl, k.models, k.maxBudget)
	if k.secondsLeft <= 0 || k.secondsLeft > ttl.Seconds()+60 {
		t.Errorf("key lifetime %.0fs, want between 0 and the configured %s", k.secondsLeft, ttl)
	}
	if k.user != user1 || k.device != pc1 {
		t.Errorf("key bound to user=%q device=%q, want %s/%s", k.user, k.device, user1, pc1)
	}
	if !strings.HasPrefix(k.alias, "abx:") {
		t.Errorf("key alias %q does not say where it came from", k.alias)
	}
	// only the models of this device's policy
	for _, m := range strings.Split(k.models, ",") {
		if m != "deepseek-v3" && m != "glm-4" && m != "small" {
			t.Errorf("key allows model %q outside the policy", m)
		}
	}
	// no key of this sandbox may have been given more than the configured lifetime
	if n := psql(t, "litellm", fmt.Sprintf(`SELECT count(*) FROM "LiteLLM_VerificationToken" WHERE metadata->>'agentbox.sandbox' = '%s'
		AND (expires IS NULL OR expires - created_at > interval '%d seconds')`, s.ID, int(ttl.Seconds())+10)); len(n) == 0 || n[0][0] != "0" {
		t.Errorf("keys of %s live longer than the configured %s (or never expire): %v", s.ID, ttl, n)
	}

	// The sandbox's own token is worthless to LiteLLM itself (it is not a LiteLLM key).
	litellm := litellmURL(t)
	if litellm != "" {
		tok := sandboxToken(t, s)
		code := httpStatus(t, "GET", litellm+"/v1/models", tok)
		if code == 200 {
			t.Errorf("the sandbox token was accepted by LiteLLM directly (status %d)", code)
		}
	}

	// Rotation: with a short test lifetime, a key close to expiry is replaced.
	if ttl > 3*time.Minute {
		t.Logf("key rotation not exercised: KEY_TTL is %s (scripts/test.sh sets 90s)", ttl)
		return
	}
	// Compare the newest key before and after, not how many exist: the gateway
	// deletes expired keys, so the count can stay the same when one is replaced.
	before := keysOf(t, s.ID)
	if len(before) == 0 {
		t.Fatal("no key to age")
	}
	oldest := before[len(before)-1].alias
	renewAt := ttl - ttl/3
	time.Sleep(renewAt + 5*time.Second)
	if c := modelCall(t, s, "deepseek-v3"); c.Code != 200 {
		t.Fatalf("model call after the key aged: %d %s", c.Code, c.Body)
	}
	after := keysOf(t, s.ID)
	if len(after) == 0 || after[len(after)-1].alias == oldest {
		t.Fatalf("the key was not replaced after %s: newest key is still %s", renewAt+5*time.Second, oldest)
	}
	if left := after[len(after)-1].secondsLeft; left < ttl.Seconds()/2 {
		t.Errorf("the replacement key has only %.0fs left", left)
	}
}

// llmClient talks to the bed's LiteLLM. A company LiteLLM behind a private CA
// (scripts/test-external-litellm.sh) is not trusted by the test runner, which is
// a throw-away container on the test network: verification is off here only.
func llmClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
}

func litellmURL(t *testing.T) string {
	u := envOr("E2E_LITELLM_URL", dockerEnv(t, "gatekeeper")["LITELLM_URL"])
	if u == "" {
		return ""
	}
	// reachable from where the tests run? (they run on the stack network in scripts/test.sh)
	c := llmClient(5 * time.Second)
	resp, err := c.Get(strings.TrimRight(u, "/") + "/health/liveliness")
	if err != nil {
		t.Logf("LiteLLM at %s is not reachable from the test runner: %v", u, err)
		return ""
	}
	resp.Body.Close()
	return strings.TrimRight(u, "/")
}

func httpStatus(t *testing.T, method, u, bearer string) int {
	req, _ := http.NewRequest(method, u, nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := llmClient(15 * time.Second).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, u, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// ---------- E2E-19: a device that loses trust loses its keys ----------

func TestE2E_19_RevokedDeviceLosesKeysAtOnce(t *testing.T) {
	if !hasSvc(app2) || !running(app2) {
		t.Skip("needs the second PC and appliance (pc-2): run scripts/test.sh or start appliance-2 and pc-2")
	}
	ensureEnrolled(t, pc2, user2)
	s := ensureSandbox(t, pc2, "e2e-revoke", "codex")
	if c := modelCall(t, s, "deepseek-v3"); c.Code != 200 {
		t.Fatalf("model call before the revoke: %d %s", c.Code, c.Body)
	}
	if n := len(keysOf(t, s.ID)); n == 0 {
		t.Fatal("no key was minted for the sandbox")
	}
	t.Cleanup(func() { enroll(t, pc2, user2) }) // an admin-reviewed re-enrollment

	cn := consoleAs(t, "bob")
	if code, body := cn.post(t, "POST", "/api/devices/"+pc2+"/revoke", map[string]any{"reason": "E2E-19: device lost"}); code >= 300 {
		t.Fatalf("revoke: %d %s", code, body)
	}
	t0 := time.Now()
	waitFor(t, 30*time.Second, "the revoked device's LiteLLM keys to be deleted", func() (bool, string) {
		n := len(keysOf(t, s.ID))
		return n == 0, fmt.Sprintf("%d keys left", n)
	})
	t.Logf("PERF keys gone %s after the revoke", time.Since(t0).Round(100*time.Millisecond))
	if c := modelCall(t, s, "deepseek-v3"); c.Code == 200 {
		t.Errorf("a revoked device still got a model answer")
	} else if !strings.Contains(c.Body, "revoked") && c.Code != 403 {
		t.Errorf("revoked device: status %d body %q, want a 403 that says it was revoked", c.Code, c.Body)
	}
	if n := len(keysOf(t, s.ID)); n != 0 {
		t.Errorf("%d new keys were minted for a revoked device", n)
	}
}

// ---------- E2E-20: the readiness check the LiteLLM maintainers run ----------

func TestE2E_20_LiteLLMPreflight(t *testing.T) {
	u := litellmURL(t)
	if u == "" {
		t.Skip("LiteLLM is not reachable from the test runner")
	}
	key := masterKey(t)
	if key == "" {
		t.Skip("cannot read the LiteLLM master key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	res := llmcheck.Run(ctx, llmcheck.Config{URL: u, MasterKey: key, Models: []string{"deepseek-v3", "glm-4"}, TTL: gatekeeperTTL(t), Completions: true, Expiry: true, Client: llmClient(60 * time.Second)})
	for _, r := range res {
		t.Logf("[%s] %s %s", r.Status, r.Name, r.Detail)
		if r.Status == llmcheck.Fail || r.Status == llmcheck.Warn {
			t.Errorf("preflight %s: %s: %s (fix: %s)", r.Status, r.Name, r.Detail, r.Fix)
		}
	}
	// a wrong master key must be reported, not silently accepted
	bad := llmcheck.Run(ctx, llmcheck.Config{URL: u, MasterKey: "sk-not-the-key", Models: []string{"deepseek-v3"}, Client: llmClient(60 * time.Second)})
	if !llmcheck.Failed(bad) {
		t.Errorf("the check passed with a wrong master key: %v", bad)
	}
}
