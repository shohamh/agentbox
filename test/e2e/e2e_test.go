//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The first PC under test defaults to the simulated pc-1. To run the same
// suite against a native PC (for example Windows, "pc-win"), set E2E_PC1 and
// E2E_APP1 (the appliance container serving it) and AGENTBOX_EXE.
var (
	pc1, app1, user1 = envOr("E2E_PC1", "pc-1"), envOr("E2E_APP1", "appliance-1"), envOr("E2E_USER1", "alice")
	pc2, app2, user2 = "pc-2", "appliance-2", "dave"
)

// ---------- E2E-01 / E2E-02: enrollment ----------

func enrollAndAssert(t *testing.T, pc, user string) {
	start := time.Now()
	enroll(t, pc, user)
	cn := consoleAs(t, "bob")
	waitFor(t, 60*time.Second, pc+" shown as enrolled and attested in the console", func() (bool, string) {
		d := cn.device(t, pc)
		if d == nil {
			return false, "device not listed"
		}
		return str(d["attestation_status"]) == "pass" && d["revoked"] != true, fmt.Sprintf("status=%v", d["attestation_status"])
	})
	t.Logf("PERF enroll to attested in console: %s (limit 60s)", time.Since(start).Round(time.Millisecond))
	d := cn.device(t, pc)
	if str(d["username"]) != user {
		t.Errorf("device owner = %v, want %s", d["username"], user)
	}
	if str(d["appliance_version"]) == "" {
		t.Errorf("console shows no appliance version for %s", pc)
	}
	detail := sub(d, "attestation_detail")
	if str(detail["result"]) != "pass" || str(detail["pcr11"]) == "" {
		t.Errorf("attestation evidence missing in console: %v", detail)
	}
	r := cli(t, pc, "status")
	if !strings.Contains(r.Out, "attestation pass") {
		t.Errorf("agentbox status does not report a passing attestation:\n%s", r.Out)
	}
	// every enrollment is in the audit log
	found := false
	for _, a := range cn.list(t, "/api/audit?q="+url.QueryEscape(pc)) {
		found = found || str(a["action"]) == "device.enrolled"
	}
	if !found {
		t.Errorf("no device.enrolled audit entry for %s", pc)
	}
}

func TestE2E_01_InstallEnrollSignIn(t *testing.T) { enrollAndAssert(t, pc1, user1) }

// E2E-02: the Linux package path. The test bed's PC containers stand in for a
// deb/rpm install; a second Linux PC must enroll and attest identically.
func TestE2E_02_EnrollSecondLinuxPC(t *testing.T) { enrollAndAssert(t, pc2, user2) }

// ---------- E2E-03 / E2E-04: agent tasks ----------

// waitTaskTelemetry waits until model calls, tool calls and process events for
// the sandbox are visible under the user's name, in ClickHouse and the console.
func waitTaskTelemetry(t *testing.T, s sbInfo, since time.Time, user, device string) {
	t.Helper()
	var rows []map[string]any
	waitFor(t, 120*time.Second, "model, tool and process events for "+s.ID, func() (bool, string) {
		rows = events(t, since, sbExpr+" = {sb:String}", map[string]string{"sb": s.ID})
		n := map[string]int{}
		for _, r := range rows {
			n[str(r["event"])]++
		}
		return n["model.call"] >= 3 && n["gen_ai.tool_call"] >= 1 && n["process.exec"] >= 1, fmt.Sprint(n)
	})
	for _, r := range rows {
		switch str(r["event"]) {
		case "model.call", "gen_ai.tool_call", "process.exec":
			if str(r["user"]) != user || str(r["device"]) != device {
				t.Errorf("%s event stamped user=%q device=%q, want %s/%s", r["event"], r["user"], r["device"], user, device)
			}
		}
	}
	// the same timeline through the console API
	var tl []map[string]any
	consoleAs(t, "bob").get(t, "/api/activity?hours=1&limit=2000&sandbox="+s.ID, &tl)
	seen := map[string]bool{}
	for _, r := range tl {
		seen[str(r["event"])] = true
	}
	for _, want := range []string{"model.call", "gen_ai.tool_call", "process.exec"} {
		if !seen[want] {
			t.Errorf("console activity timeline for %s has no %s", s.ID, want)
		}
	}
}

func agentTask(t *testing.T, name, image, harness string) (sbInfo, time.Time) {
	ensureEnrolled(t, pc1, user1)
	s := ensureSandbox(t, pc1, name, image)
	sbRun(t, s, 30*time.Second, "rm -f /workspace/test_agent_added.py")
	r, t0 := runTask(t, s, harness, unitTestTask)
	if r.Code != 0 {
		t.Fatalf("%s exec failed (%d):\n%s", harness, r.Code, r.Out)
	}
	if p := suitePasses(t, s); p.Code != 0 {
		t.Fatalf("the agent's test does not exist or fails in the sandbox:\n%s", p.Out)
	}
	waitTaskTelemetry(t, s, t0, user1, pc1)
	return s, t0
}

func TestE2E_03_CodexTask(t *testing.T) { agentTask(t, "e2e-codex", "codex", "codex") }

// modelCallFields returns the agentbox.* and gen_ai.* attribute keys on model.call events.
func modelCallFields(t *testing.T, sandbox string) map[string]bool {
	rows := events(t, time.Now().Add(-24*time.Hour), evExpr+" = 'model.call' AND "+sbExpr+" = {sb:String}", map[string]string{"sb": sandbox})
	set := map[string]bool{}
	for _, r := range rows {
		for k := range sub(r, "attrs") {
			if strings.HasPrefix(k, "agentbox.") || strings.HasPrefix(k, "gen_ai.") {
				set["attr:"+k] = true
			}
		}
		for k := range sub(r, "res") {
			if strings.HasPrefix(k, "agentbox.") {
				set["res:"+k] = true
			}
		}
	}
	return set
}

func TestE2E_04_OpencodeTask(t *testing.T) {
	oc, _ := agentTask(t, "e2e-opencode", "opencode", "opencode")
	cx := findSandbox(t, pc1, "e2e-codex")
	if cx == nil || len(modelCallFields(t, cx.ID)) == 0 {
		agentTask(t, "e2e-codex", "codex", "codex") // run alone: produce the codex telemetry to compare with
		cx = findSandbox(t, pc1, "e2e-codex")
	}
	a, b := modelCallFields(t, cx.ID), modelCallFields(t, oc.ID)
	for k := range a {
		if !b[k] {
			t.Errorf("model.call field %s is present for codex but missing for opencode", k)
		}
	}
	for k := range b {
		if !a[k] {
			t.Errorf("model.call field %s is present for opencode but missing for codex", k)
		}
	}
	// The user and device must be stamped on every model call, as a resource
	// attribute or (LiteLLM callback records) as a log attribute.
	for _, need := range []string{"agentbox.user", "agentbox.device", "agentbox.sandbox"} {
		if !a["res:"+need] && !a["attr:"+need] {
			t.Errorf("model.call events lack required %s (have %v)", need, a)
		}
	}
}

// ---------- E2E-05: interactive sessions ----------

func TestE2E_05_InteractiveSessions(t *testing.T) {
	ensureEnrolled(t, pc1, user1)
	s := ensureSandbox(t, pc1, "e2e-codex", "codex")

	// VS Code Remote-SSH and JetBrains Gateway use the managed ssh config include.
	if r := cli(t, pc1, "code", s.Name); r.Code != 0 {
		t.Fatalf("agentbox code: %s", r.Out)
	}
	r := pcSSH(t, pc1, s.Name, "echo remote-ssh-ok; cd /workspace && pwd")
	if r.Code != 0 || !strings.Contains(r.Out, "remote-ssh-ok") || !strings.Contains(r.Out, "/workspace") {
		t.Fatalf("ssh through the generated Remote-SSH entry failed (%d): %s", r.Code, r.Out)
	}

	// latency of an interactive command through the ProxyCommand tunnel
	var lat []float64
	for i := 0; i < 15; i++ {
		x := cli(t, pc1, "shell", s.Name, "--", "true")
		if x.Code != 0 {
			t.Fatalf("shell attempt %d failed: %s", i, x.Out)
		}
		lat = append(lat, float64(x.Dur.Milliseconds()))
	}
	perf(t, "agentbox shell round trip p95", p95(lat), 3000, "ms")

	// P3: gateway overhead against calling the model server directly
	via := modelCalls(t, s, "deepseek-v3", 30, "")
	var viaMs []float64
	for _, c := range via {
		if c.Code != 200 {
			t.Fatalf("model call through the gateway failed: %d %s", c.Code, c.Body)
		}
		viaMs = append(viaMs, c.Ms)
	}
	mock := envOr("MOCK_URL", "http://mockllm:8000")
	cl := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 30 * time.Second}
	var directMs []float64
	body := `{"model":"deepseek-v3","messages":[{"role":"user","content":"ping"}]}`
	for i := 0; i < 30; i++ {
		st := time.Now()
		resp, err := cl.Post(mock+"/v1/chat/completions", "application/json", strings.NewReader(body))
		if err != nil {
			t.Logf("direct baseline unavailable (%v); skipping P3 overhead", err)
			directMs = nil
			break
		}
		resp.Body.Close()
		directMs = append(directMs, float64(time.Since(st).Microseconds())/1000)
	}
	if directMs != nil {
		perf(t, "P3 model path overhead (p95 via gateway - p95 direct)", p95(viaMs)-p95(directMs), 50, "ms")
	}

	// soak: steady mixed use, no failures
	secs, _ := strconv.Atoi(envOr("E2E05_SOAK_SECS", "45"))
	end := time.Now().Add(time.Duration(secs) * time.Second)
	n := 0
	for time.Now().Before(end) {
		if x := cli(t, pc1, "shell", s.Name, "--", "true"); x.Code != 0 {
			t.Fatalf("soak: shell failed after %d rounds: %s", n, x.Out)
		}
		if c := modelCall(t, s, "deepseek-v3"); c.Code != 200 {
			t.Fatalf("soak: model call failed after %d rounds: %d %s", n, c.Code, c.Body)
		}
		n++
		time.Sleep(3 * time.Second)
	}
	t.Logf("soak: %d rounds over %ds without failure (the 30-minute manual session is a separate checklist item)", n, secs)
}

// ---------- E2E-06: no key anywhere; token replay ----------

const hitScript = `D=/tmp/t.dump.sh
{ env
  for p in /proc/[0-9]*; do tr '\0' '\n' < $p/environ 2>/dev/null; tr '\0' '\n' < $p/cmdline 2>/dev/null; done
  find /etc /root /workspace /home /var/lib/agentbox /run /tmp -type f ! -name 't.*.sh' -size -2M 2>/dev/null | while read f; do cat "$f" 2>/dev/null; echo; done
} > $D 2>/dev/null
printf '%s\n' 'sk-[A-Za-z0-9_-]{20,}' > /tmp/t.re.sh
echo "SK_HITS=$(grep -Ec -f /tmp/t.re.sh $D)"
MASTER_PLACEHOLDER
rm -f $D /tmp/t.re.sh /tmp/t.pat.sh
`

// scanFor runs the key search in a container with the script on disk, so the
// searched-for strings never appear in a process command line.
func scanFor(t *testing.T, svc, master string, viaSandbox *sbInfo) string {
	if viaSandbox == nil && isNative(svc) {
		return nativeScan(t, master)
	}
	script := hitScript
	if master != "" {
		script = strings.Replace(script, "MASTER_PLACEHOLDER", "printf '%s\\n' '"+master+"' > /tmp/t.pat.sh\necho \"MASTER_HITS=$(grep -c -F -f /tmp/t.pat.sh $D)\"", 1)
	} else {
		script = strings.Replace(script, "MASTER_PLACEHOLDER", "echo MASTER_HITS=0", 1)
	}
	if viaSandbox != nil {
		r := sbRun(t, *viaSandbox, 3*time.Minute, script)
		return r.Out
	}
	if r := dexec(t, svc, 30*time.Second, script, "sh", "-c", "cat > /tmp/t.x.sh"); r.Code != 0 {
		t.Fatalf("copy scan script to %s: %s", svc, r.Out)
	}
	r := dexec(t, svc, 3*time.Minute, "", "sh", "/tmp/t.x.sh")
	dexec(t, svc, 10*time.Second, "", "rm", "-f", "/tmp/t.x.sh")
	return r.Out
}

func masterKey(t *testing.T) string {
	e := dockerEnv(t, "gatekeeper")
	if f := e["LITELLM_MASTER_KEY_FILE"]; f != "" {
		if r := dexec(t, "gatekeeper", 15*time.Second, "", "cat", f); r.Code == 0 && r.Out != "" {
			return strings.TrimSpace(r.Out)
		}
	}
	if hasSvc("litellm") {
		if k := dockerEnv(t, "litellm")["LITELLM_MASTER_KEY"]; k != "" {
			return k
		}
	}
	t.Log("could not read the LiteLLM master key; scanning for the sk- key pattern only")
	return ""
}

var statusRe = regexp.MustCompile(`HTTP/1\.[01] (\d{3})`)

// rawModelCall posts to the local model endpoint from the appliance's own
// network namespace (not from a sandbox) with the given bearer token.
func rawModelCall(t *testing.T, appliance, token string) (int, string) {
	script := `exec 3<>/dev/tcp/10.88.0.1/4000
BODY='{"model":"deepseek-v3","messages":[{"role":"user","content":"relay"}]}'
printf 'POST /v1/chat/completions HTTP/1.1\r\nHost: llm.agentbox\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s' "$1" ${#BODY} "$BODY" >&3
timeout 10 cat <&3`
	r := dexec(t, appliance, 30*time.Second, "", "bash", "-c", script, "x", token)
	m := statusRe.FindStringSubmatch(r.Out)
	if m == nil {
		return 0, r.Out
	}
	code, _ := strconv.Atoi(m[1])
	return code, r.Out
}

func sandboxToken(t *testing.T, s sbInfo) string {
	r := cli(t, s.PC, "key", s.Name)
	for _, l := range strings.Split(r.Out, "\n") {
		if v, ok := strings.CutPrefix(l, "AGENTBOX_TOKEN="); ok {
			return strings.TrimSpace(v)
		}
	}
	t.Fatalf("agentbox key printed no token: %s", r.Out)
	return ""
}

func TestE2E_06_NoKeyAnywhereAndTokenReplay(t *testing.T) {
	ensureEnrolled(t, pc1, user1)
	s1 := ensureSandbox(t, pc1, "e2e-codex", "codex")
	s2 := ensureSandbox(t, pc1, "e2e-opencode", "opencode")
	if c := modelCall(t, s1, "deepseek-v3"); c.Code != 200 {
		t.Fatalf("baseline model call failed: %d %s", c.Code, c.Body)
	}
	master := masterKey(t)
	for _, where := range []struct {
		name string
		svc  string
		sb   *sbInfo
	}{{"sandbox", "", &s1}, {"appliance", app1, nil}, {"PC", pc1, nil}} {
		out := scanFor(t, where.svc, master, where.sb)
		if !strings.Contains(out, "MASTER_HITS=0") || !strings.Contains(out, "SK_HITS=0") {
			t.Errorf("model credential found in the %s (environment, files or process memory): %s", where.name, out)
		}
	}

	tok := sandboxToken(t, s1)
	since := time.Now()
	// replay from the appliance itself (the "host"), from another sandbox, and from another PC
	if code, out := rawModelCall(t, app1, tok); code != 403 || !strings.Contains(out, "token_relay") {
		t.Errorf("replay from the appliance host: status %d, body %q; want 403 token_relay", code, out)
	}
	if c := modelCalls(t, s2, "deepseek-v3", 1, tok)[0]; c.Code != 403 || !strings.Contains(c.Body, "token_relay") {
		t.Errorf("replay from another sandbox: %d %s; want 403 token_relay", c.Code, c.Body)
	}
	if hasSvc(app2) && running(app2) {
		if code, out := rawModelCall(t, app2, tok); code != 403 {
			t.Errorf("replay from another PC's appliance: status %d %q; want 403", code, out)
		}
	}
	// the token still works from its own sandbox
	if c := modelCall(t, s1, "deepseek-v3"); c.Code != 200 {
		t.Errorf("own sandbox lost model access after replays: %d %s", c.Code, c.Body)
	}
	waitFor(t, 90*time.Second, "token.relay event and a D4 relay alert for "+s1.ID, func() (bool, string) {
		ev := countEvents(t, since, "token.relay", s1.ID)
		al := consoleAs(t, "bob").alerts(t, "D4", "", s1.ID)
		return ev >= 1 && len(al) >= 1, fmt.Sprintf("events=%d alerts=%d", ev, len(al))
	})
}

// ---------- E2E-07: Codex's own sandbox under gVisor ----------

func TestE2E_07_CodexInnerSandbox(t *testing.T) {
	ensureEnrolled(t, pc1, user1)
	s := ensureSandbox(t, pc1, "e2e-bwrap", "codex")
	sbRun(t, s, 30*time.Second, "rm -f /workspace/test_agent_added.py")
	r := sbRun(t, s, 4*time.Minute, `codex exec --skip-git-repo-check -s workspace-write "`+unitTestTask+`"`)
	p := suitePasses(t, s)
	works := r.Code == 0 && p.Code == 0
	tail := r.Out
	if len(tail) > 600 {
		tail = tail[len(tail)-600:]
	}
	// Recorded, never failed: the decision on danger-full-access stays the default unless this works.
	t.Logf("RECORD E2E-07 codex workspace-write (bubblewrap) under gVisor: works=%v exit=%d suite=%d\n%s", works, r.Code, p.Code, tail)
	if works {
		t.Log("RECORD E2E-07 decision: bubblewrap works under gVisor here; the team may turn on workspace-write")
	} else {
		t.Log("RECORD E2E-07 decision: keep sandbox_mode = danger-full-access (gVisor is the boundary)")
	}
}

// ---------- E2E-08: egress ----------

const egressScript = `python3 - <<'PY'
import urllib.request, urllib.error
try:
    r = urllib.request.urlopen("http://%s/", timeout=10); print("CODE", r.status)
except urllib.error.HTTPError as e:
    print("CODE", e.code, e.read().decode()[:300])
except Exception as e:
    print("ERR", e)
PY`

func TestE2E_08_EgressBlocked(t *testing.T) {
	ensureEnrolled(t, pc1, user1)
	s := ensureSandbox(t, pc1, "e2e-codex", "codex")
	since := time.Now()
	r := sbRun(t, s, 60*time.Second, fmt.Sprintf(egressScript, "blocked.agentbox.corp"))
	if !strings.Contains(r.Out, "CODE 403") && !strings.Contains(r.Out, "ERR") {
		t.Fatalf("request to a host outside the egress list was not blocked: %s", r.Out)
	}
	if strings.Contains(r.Out, "CODE 200") {
		t.Fatalf("blocked host answered 200: %s", r.Out)
	}
	t.Logf("blocked request result: %s", r.Out)
	waitFor(t, 90*time.Second, "egress.denied event, D7 alert and timeline entry", func() (bool, string) {
		rows := events(t, since, evExpr+" = 'egress.denied' AND "+sbExpr+" = {sb:String}", map[string]string{"sb": s.ID})
		hit := false
		for _, e := range rows {
			hit = hit || strings.Contains(str(sub(e, "attrs")["host"]), "blocked.agentbox.corp")
		}
		al := consoleAs(t, "bob").alerts(t, "D7", "", s.ID)
		return hit && len(al) > 0, fmt.Sprintf("events=%d alerts=%d", len(rows), len(al))
	})
	var tl []map[string]any
	consoleAs(t, "bob").get(t, "/api/activity?hours=1&limit=500&kinds=egress&sandbox="+s.ID, &tl)
	if len(tl) == 0 {
		t.Errorf("the sandbox activity timeline has no egress entry")
	}
	// a host in the group's default egress list is not refused by the proxy
	if ok := sbRun(t, s, 60*time.Second, fmt.Sprintf(egressScript, "git.agentbox.corp")); strings.Contains(ok.Out, "CODE 403") && strings.Contains(ok.Out, "egress") {
		t.Errorf("an allowed egress host was blocked by policy: %s", ok.Out)
	}
}

// ---------- E2E-09: control plane cut mid-task ----------

func controlPlaneIPs(t *testing.T, appliance string) []string {
	r := dexec(t, appliance, 15*time.Second, "", "cat", "/etc/agentbox/appliance.json")
	var cfg map[string]any
	if err := json.Unmarshal([]byte(r.Out), &cfg); err != nil {
		t.Fatalf("appliance.json: %v: %s", err, r.Out)
	}
	hosts := map[string]bool{}
	for _, k := range []string{"enroll_url", "permsvc_url", "telemetry_url", "registry", "gateway_host"} {
		v, _ := cfg[k].(string)
		if v == "" {
			continue
		}
		if !strings.Contains(v, "://") {
			v = "tcp://" + v
		}
		if u, err := url.Parse(v); err == nil && u.Hostname() != "" {
			hosts[u.Hostname()] = true
		}
	}
	var ips []string
	for h := range hosts {
		if net.ParseIP(h) != nil {
			ips = append(ips, h)
			continue
		}
		g := dexec(t, appliance, 15*time.Second, "", "getent", "ahostsv4", h)
		if f := strings.Fields(g.Out); len(f) > 0 {
			ips = append(ips, f[0])
		}
	}
	if len(ips) == 0 {
		t.Fatalf("no control-plane addresses found in %v", cfg)
	}
	return ips
}

func cutControlPlane(t *testing.T, appliance string, ips []string) {
	script := fmt.Sprintf("nft add table inet e2ecut && nft add chain inet e2ecut out '{ type filter hook output priority -150; policy accept; }' && nft add rule inet e2ecut out ip daddr '{ %s }' drop", strings.Join(ips, ", "))
	if r := sh(t, appliance, script); r.Code != 0 {
		t.Fatalf("cutting the control plane: %s", r.Out)
	}
}

func restoreControlPlane(appliance string) {
	if c, ok := cidOf(appliance); ok {
		docker("exec", c, "nft", "delete", "table", "inet", "e2ecut")
	}
}

func clearMessage(t *testing.T, c mc) {
	t.Helper()
	low := strings.ToLower(c.Body)
	if msgStrict {
		if !strings.Contains(low, "model service unreachable") {
			t.Errorf("error body is not the clear 'model service unreachable' message: %q", c.Body)
		}
		return
	}
	if strings.TrimSpace(c.Body) == "" || !(strings.Contains(low, "unreachable") || strings.Contains(low, "unavailable") || strings.Contains(low, "upstream") || strings.Contains(low, "timed out") || strings.Contains(low, "timeout")) {
		t.Errorf("model call failure message is empty or unclear: %q (STRICT_MESSAGES=1 requires 'model service unreachable')", c.Body)
	}
}

func TestE2E_09_ControlPlaneCutMidTask(t *testing.T) {
	ensureEnrolled(t, pc1, user1)
	s := ensureSandbox(t, pc1, "e2e-codex", "codex")
	if c := modelCall(t, s, "deepseek-v3"); c.Code != 200 {
		t.Fatalf("baseline model call: %d %s", c.Code, c.Body)
	}
	ips := controlPlaneIPs(t, app1)
	t.Cleanup(func() { restoreControlPlane(app1) })

	done := make(chan Result, 1)
	go func() {
		done <- sbRun(t, s, 5*time.Minute, `codex exec --skip-git-repo-check "RUN: sleep 20 && echo mid-task-finished"`)
	}()
	time.Sleep(5 * time.Second)
	cutControlPlane(t, app1, ips)
	cutAt := time.Now()

	// sandboxes keep running
	if st := findSandbox(t, pc1, s.Name); st == nil || st.State != "running" {
		t.Fatalf("sandbox stopped when the control plane was cut: %v", st)
	}
	if r := cli(t, pc1, "shell", s.Name, "--", "echo", "still-alive"); r.Code != 0 || !strings.Contains(r.Out, "still-alive") {
		t.Fatalf("sandbox shell broke during the outage: %s", r.Out)
	}
	// model calls fail, with a message
	c := modelCall(t, s, "deepseek-v3")
	if c.Code == 200 {
		t.Fatalf("model call succeeded while the control plane was cut")
	}
	t.Logf("during the outage a model call fails with %d after %.0f ms: %s", c.Code, c.Ms, c.Body)
	clearMessage(t, c)
	// the PC reports the outage
	waitFor(t, 150*time.Second, "agentbox status reporting the control plane unreachable", func() (bool, string) {
		r := cli(t, pc1, "status")
		return strings.Contains(r.Out, "control     UNREACHABLE"), r.Out
	})
	t.Logf("outage detected %s after the cut", time.Since(cutAt).Round(time.Second))

	// restore: the PC recovers on its own (O3)
	restoreControlPlane(app1)
	waitFor(t, 180*time.Second, "model calls and control plane status to recover without intervention", func() (bool, string) {
		r := cli(t, pc1, "status")
		if !strings.Contains(r.Out, "control     reachable") {
			return false, r.Out
		}
		c := modelCall(t, s, "deepseek-v3")
		return c.Code == 200, fmt.Sprintf("%d %s", c.Code, c.Body)
	})
	select {
	case r := <-done:
		t.Logf("the mid-task Codex run ended (exit %d) and did not hang: %s", r.Code, truncate(r.Out, 300))
	case <-time.After(4 * time.Minute):
		t.Errorf("the mid-task Codex run never ended")
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// ---------- E2E-10: modified appliance image ----------

var (
	tamperOnce sync.Once
	tamperDev  map[string]any
)

// tamperedDevice enrolls pc-t (whose appliance runs a modified image) and
// returns the console's failed-attestation record.
func tamperedDevice(t *testing.T) map[string]any {
	t.Helper()
	tamperOnce.Do(func() {
		if !hasSvc("pc-t") || !running("appliance-t") {
			return
		}
		enroll(t, "pc-t", user1)
		cn := consoleAs(t, "bob")
		waitFor(t, 120*time.Second, "pc-t attestation to fail in the console", func() (bool, string) {
			d := cn.device(t, "pc-t")
			if d != nil && str(d["attestation_status"]) == "fail" {
				tamperDev = d
				return true, ""
			}
			return false, fmt.Sprint(d["attestation_status"])
		})
	})
	if tamperDev == nil {
		t.Skip("the tamper profile (appliance-t, pc-t) is not running: start it with scripts/test.sh or `docker compose --profile tamper up -d`")
	}
	return tamperDev
}

func TestE2E_10_ModifiedApplianceRefused(t *testing.T) {
	d := tamperedDevice(t)
	detail := sub(d, "attestation_detail")
	if str(detail["result"]) != "fail" {
		t.Errorf("attestation detail result = %v, want fail", detail["result"])
	}
	t.Logf("console evidence: reason=%v diff=%v", detail["reason"], detail["diff"])
	if diff, _ := detail["diff"].([]any); len(diff) == 0 && !strings.Contains(str(detail["reason"]), "PCR 11") {
		t.Errorf("console shows no diff or PCR reason for the failed attestation: %v", detail)
	}
	if n := len(consoleAs(t, "bob").alerts(t, "D3", "pc-t", "")); n == 0 {
		t.Errorf("no D3 alert for the modified appliance")
	}
	// model access is refused: no identity, no policy bundle, no sandboxes
	r := cli(t, "pc-t", "status")
	if strings.Contains(r.Out, "attestation pass") {
		t.Errorf("modified appliance reports a passing attestation:\n%s", r.Out)
	}
	n := cli(t, "pc-t", "new", sampleRepo, "--name", "should-not-exist", "--image", "base")
	if n.Code == 0 {
		t.Errorf("a sandbox was created on the modified appliance (model access would be possible): %s", n.Out)
	}
	if !strings.Contains(n.Out, "cannot create a sandbox") {
		t.Logf("sandbox refusal message: %s", n.Out)
	}
}

var _ = bytes.NewReader
var _ = os.Getenv
