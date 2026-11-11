//go:build e2e

package e2e

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Automated rows of the tamper matrix: T1 T2 T3 T7 T9 T10 T12.
// (T4-T6 need a Linux or Windows host with a real hypervisor, T8 a second PC
// with a copied vTPM, T11 a revoked release; they are manual per release.)

func failedDevice(t *testing.T, id string) map[string]any {
	t.Helper()
	if id == "pc-t" {
		return tamperedDevice(t)
	}
	return consoleAs(t, "bob").device(t, id)
}

// T1: a binary in the appliance root differs from the signed release.
func TestT1_ModifiedBinaryInApplianceRoot(t *testing.T) {
	d := tamperedDevice(t)
	detail := sub(d, "attestation_detail")
	diff, _ := detail["diff"].([]any)
	joined := fmt.Sprint(diff)
	if str(detail["result"]) != "fail" {
		t.Fatalf("attestation did not fail: %v", detail)
	}
	if !strings.Contains(joined, "hash differs from release manifest") && !strings.Contains(joined, "not in release manifest") {
		t.Errorf("the console diff does not name the modified file: %v (reason %v)", diff, detail["reason"])
	}
	t.Logf("modified files reported by the verifier: %v", diff)
}

// T2: a modified UKI or kernel command line gives a PCR 11 that matches no release; no identity.
func TestT2_PCR11DoesNotMatchManifest(t *testing.T) {
	d := tamperedDevice(t)
	pcr := str(sub(d, "attestation_detail")["pcr11"])
	if pcr == "" {
		t.Fatalf("no PCR 11 in the evidence: %v", d["attestation_detail"])
	}
	var rel struct {
		Manifests []map[string]any `json:"manifests"`
	}
	consoleAs(t, "bob").get(t, "/api/releases", &rel)
	if len(rel.Manifests) == 0 {
		t.Fatal("the console has no signed release manifests loaded")
	}
	for _, m := range rel.Manifests {
		if str(m["pcr11"]) == pcr {
			t.Errorf("the tampered appliance's PCR 11 %s equals release %v", pcr, m["version"])
		}
	}
	r := cli(t, "pc-t", "status")
	if strings.Contains(r.Out, "attestation pass") {
		t.Errorf("no identity must be issued, but status says:\n%s", r.Out)
	}
}

// T3: the dev flavor (serial console, debug shell) is never accepted by the production verifier.
func TestT3_DevFlavorRefused(t *testing.T) {
	pc, app, user := envOr("DEV_PC", "pc-d"), envOr("DEV_APPLIANCE", "appliance-d"), envOr("DEV_USER", user1)
	if !hasSvc(pc) || !running(app) {
		t.Skipf("the dev profile (%s, %s) is not running: scripts/test.sh starts it for this test", app, pc)
	}
	enroll(t, pc, user)
	cn := consoleAs(t, "bob")
	var d map[string]any
	waitFor(t, 120*time.Second, pc+" attestation to be refused", func() (bool, string) {
		d = cn.device(t, pc)
		return d != nil && str(d["attestation_status"]) == "fail", fmt.Sprint(d["attestation_status"])
	})
	detail := sub(d, "attestation_detail")
	reason := strings.ToLower(str(detail["reason"]))
	if !strings.Contains(reason, "dev") && !strings.Contains(reason, "no published production release") {
		t.Errorf("unexpected refusal reason for the dev flavor: %v", detail["reason"])
	}
	if r := cli(t, pc, "status"); strings.Contains(r.Out, "attestation pass") {
		t.Errorf("dev appliance reports a passing attestation:\n%s", r.Out)
	}
}

// T7: replaying an old quote (nonce) fails the nonce check.
func TestT7_ReplayedQuoteRejected(t *testing.T) {
	ensureEnrolled(t, pc1, user1)
	hc := dockerHostConf(t)
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	post := func(path string, v any) (int, string) {
		b, _ := json.Marshal(v)
		resp, err := client.Post(hc+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		d, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(d)
	}
	// a throwaway device record, so the real devices' verdicts are not disturbed
	psql(t, "main", `INSERT INTO devices(device_id, username, ak_public) VALUES('pc-replay','alice','\x00'::bytea) ON CONFLICT (device_id) DO NOTHING`)
	t.Cleanup(func() {
		psql(t, "main", `DELETE FROM alerts WHERE device_id='pc-replay'`)
		psql(t, "main", `DELETE FROM nonces WHERE device_id='pc-replay'`)
		psql(t, "main", `DELETE FROM devices WHERE device_id='pc-replay'`)
	})
	nonce := func() string {
		code, body := post("/api/v1/attest/nonce", map[string]string{"device_id": "pc-replay"})
		var n struct {
			Nonce string `json:"nonce"`
		}
		if code != 200 || json.Unmarshal([]byte(body), &n) != nil || n.Nonce == "" {
			t.Fatalf("nonce request: %d %s", code, body)
		}
		return n.Nonce
	}
	attest := func(n string) (int, string) {
		return post("/api/v1/attest", map[string]any{"device_id": "pc-replay", "nonce": n, "quote": map[string]any{}, "event_log": []any{}, "csr": ""})
	}
	const nonceErr = "nonce unknown, reused or expired"

	n1 := nonce()
	code, body := attest(n1)
	if code != 403 || strings.Contains(body, nonceErr) {
		t.Fatalf("first use of a fresh nonce should reach quote verification and fail there: %d %s", code, body)
	}
	if code, body = attest(n1); code != 403 || !strings.Contains(body, nonceErr) {
		t.Errorf("replayed nonce: %d %s; want 403 with %q", code, body, nonceErr)
	}
	n2 := nonce()
	psql(t, "main", fmt.Sprintf(`UPDATE nonces SET created_at = now() - interval '3 minutes' WHERE nonce='%s'`, n2))
	if code, body = attest(n2); code != 403 || !strings.Contains(body, nonceErr) {
		t.Errorf("expired nonce: %d %s", code, body)
	}
	if code, body = attest(strings.Repeat("ab", 32)); code != 403 || !strings.Contains(body, nonceErr) {
		t.Errorf("never-issued nonce: %d %s", code, body)
	}
	// the nonce of a real, earlier attestation of pc-1
	old := str(sub(consoleAs(t, "bob").device(t, pc1), "attestation_detail")["nonce"])
	if old != "" {
		if code, body = attest(old); code != 403 || !strings.Contains(body, nonceErr) {
			t.Errorf("nonce from an earlier real attestation: %d %s", code, body)
		}
	}
}

func dockerHostConf(t *testing.T) string {
	r := dexec(t, pc1, 15*time.Second, "", "cat", "/etc/agentbox/host.json")
	var cfg struct {
		EnrollURL string `json:"enroll_url"`
	}
	if err := json.Unmarshal([]byte(r.Out), &cfg); err != nil || cfg.EnrollURL == "" {
		t.Fatalf("host.json on %s has no enroll_url: %v %s", pc1, err, r.Out)
	}
	return strings.TrimRight(cfg.EnrollURL, "/")
}

// T9: a sandbox token used from the host or from another sandbox is refused and raises D4.
func TestT9_SandboxTokenReplayRefused(t *testing.T) {
	ensureEnrolled(t, pc1, user1)
	s1 := ensureSandbox(t, pc1, "e2e-codex", "codex")
	s2 := ensureSandbox(t, pc1, "e2e-opencode", "opencode")
	tok := sandboxToken(t, s1)
	since := time.Now()
	if code, out := rawModelCall(t, app1, tok); code != 403 {
		t.Errorf("token replayed from the appliance host: %d %s", code, out)
	}
	if c := modelCalls(t, s2, "deepseek-v3", 1, tok)[0]; c.Code != 403 {
		t.Errorf("token replayed from another sandbox: %d %s", c.Code, c.Body)
	}
	waitFor(t, 90*time.Second, "D4 alert for "+s1.ID, func() (bool, string) {
		n := len(consoleAs(t, "bob").alerts(t, "D4", "", s1.ID))
		return n > 0 && countEvents(t, since, "token.relay", s1.ID) > 0, fmt.Sprint(n)
	})
}

func rawHTTP(t *testing.T, svc, host, port, req string) (int, string) {
	if isNative(svc) {
		out := nativeHTTP(host, port, req)
		if m := statusRe.FindStringSubmatch(out); m != nil {
			var code int
			fmt.Sscan(m[1], &code)
			return code, out
		}
		return 0, out
	}
	script := `exec 3<>/dev/tcp/$1/$2; printf '%b' "$3" >&3; timeout 3 cat <&3`
	r := dexec(t, svc, 30*time.Second, "", "bash", "-c", script, "x", host, port, req)
	m := statusRe.FindStringSubmatch(r.Out)
	if m == nil {
		return 0, r.Out
	}
	var code int
	fmt.Sscan(m[1], &code)
	return code, r.Out
}

// T10: a model-API proxy on a preview port is blocked and raises D5.
func TestT10_ModelAPIProxyOnPreviewPort(t *testing.T) {
	ensureEnrolled(t, pc1, user1)
	s := ensureSandbox(t, pc1, "e2e-codex", "codex")
	sbRun(t, s, 30*time.Second, "mkdir -p /workspace/web && echo preview-ok > /workspace/web/index.html && cd /workspace/web && (nohup python3 -m http.server 8081 --bind 0.0.0.0 >/tmp/http.log 2>&1 </dev/null &) ; sleep 1")
	killLoop := `for p in /proc/[0-9]*; do tr '\0' ' ' < $p/cmdline 2>/dev/null | grep -q '%s' && kill ${p#/proc/} 2>/dev/null; done; true`
	t.Cleanup(func() {
		sbRun(t, s, 20*time.Second, fmt.Sprintf(killLoop, "http.server 8081"))
		stopPreviews(t, pc1)
	})
	if r := startPreview(t, s, "8081", "127.0.0.1:18081"); r.Code != 0 {
		t.Fatalf("starting the preview: %s", r.Out)
	}
	waitFor(t, 30*time.Second, "the preview to serve the web app", func() (bool, string) {
		code, out := rawHTTP(t, pc1, "127.0.0.1", "18081", `GET / HTTP/1.1\r\nHost: preview\r\nConnection: close\r\n\r\n`)
		return code == 200 && strings.Contains(out, "preview-ok"), out
	})
	since := time.Now()
	body := `{"model":"deepseek-v3","messages":[{"role":"user","content":"hi"}]}`
	req := fmt.Sprintf(`POST /v1/chat/completions HTTP/1.1\r\nHost: preview\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s`, len(body), strings.ReplaceAll(body, "%", "%%"))
	code, out := rawHTTP(t, pc1, "127.0.0.1", "18081", req)
	if code != 403 || !strings.Contains(out, "model-API-shaped") {
		t.Errorf("model-API-shaped request through the preview port: %d %s; want 403 'model-API-shaped'", code, out)
	}
	waitFor(t, 90*time.Second, "preview.blocked event and D5 alert", func() (bool, string) {
		n := len(consoleAs(t, "bob").alerts(t, "D5", "", s.ID))
		return n > 0 && countEvents(t, since, "preview.blocked", s.ID) > 0, fmt.Sprint(n)
	})
}

// T12: editing the policy bundle on disk fails the signature check; the last good bundle is what applies.
func TestT12_EditedBundleOnDisk(t *testing.T) {
	ensureEnrolled(t, pc1, user1)
	s := ensureSandbox(t, pc1, "e2e-codex", "codex")
	r := dexec(t, app1, 15*time.Second, "", "cat", "/etc/agentbox/appliance.json")
	var cfg struct {
		DataDir string `json:"data_dir"`
	}
	if err := json.Unmarshal([]byte(r.Out), &cfg); err != nil || cfg.DataDir == "" {
		t.Fatalf("appliance.json data_dir: %v %s", err, r.Out)
	}
	stateFile := cfg.DataDir + "/state.json"
	raw := dexec(t, app1, 15*time.Second, "", "cat", stateFile)
	var st map[string]any
	if err := json.Unmarshal([]byte(raw.Out), &st); err != nil {
		t.Fatalf("state.json: %v", err)
	}
	b, _ := st["bundle"].(map[string]any)
	if b == nil {
		t.Fatalf("no bundle in %s", stateFile)
	}
	payload, _ := base64.StdEncoding.DecodeString(str(b["payload"]))
	var pb map[string]any
	json.Unmarshal(payload, &pb)
	eg, _ := pb["egress"].([]any)
	pb["egress"] = append(eg, "evil.example.com")
	np, _ := json.Marshal(pb)
	b["payload"] = base64.StdEncoding.EncodeToString(np) // signature left as it was
	out, _ := json.Marshal(st)
	if w := dexec(t, app1, 15*time.Second, string(out), "sh", "-c", "cat > "+stateFile+".tmp && mv "+stateFile+".tmp "+stateFile); w.Code != 0 {
		t.Fatalf("editing the bundle on disk: %s", w.Out)
	}
	before := docker("inspect", "-f", "{{.State.StartedAt}}", cid(t, app1)).Out
	cli(t, pc1, "recycle-appliance")
	waitFor(t, 90*time.Second, "the appliance to restart", func() (bool, string) {
		now := docker("inspect", "-f", "{{.State.Running}} {{.State.StartedAt}}", cid(t, app1)).Out
		if strings.HasPrefix(now, "false") {
			docker("start", cid(t, app1)) // no restart policy in this bed
		}
		return strings.HasPrefix(now, "true") && !strings.HasSuffix(now, before), now
	})
	var published string
	waitFor(t, 240*time.Second, "the appliance to attest again and hold the last good bundle", func() (bool, string) {
		r := cli(t, pc1, "status")
		return strings.Contains(r.Out, "attestation pass") && strings.Contains(r.Out, "policy      bundle v"), r.Out
	})
	status := cli(t, pc1, "status").Out
	if strings.Contains(status, "evil.example.com") {
		t.Fatalf("the edited bundle was applied:\n%s", status)
	}
	if d := consoleAs(t, "bob").device(t, pc1); d != nil {
		published = fmt.Sprint(d["published_bundle"])
	}
	if published != "" && !strings.Contains(status, "bundle v"+published+",") {
		t.Errorf("the applied bundle is not the published one (console says v%s):\n%s", published, status)
	}
	again := dexec(t, app1, 15*time.Second, "", "cat", stateFile)
	if strings.Contains(again.Out, base64.StdEncoding.EncodeToString(np)) {
		t.Errorf("the edited payload is still on disk after recovery")
	}
	// the edited egress rule does not work either
	ensureSandbox(t, pc1, s.Name, "codex")
	r2 := sbRun(t, s, 60*time.Second, fmt.Sprintf(egressScript, "evil.example.com"))
	if strings.Contains(r2.Out, "CODE 200") {
		t.Errorf("evil.example.com reachable after the bundle edit: %s", r2.Out)
	}
}
