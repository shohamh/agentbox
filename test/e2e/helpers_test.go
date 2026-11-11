//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- configuration ----------

const password = "Passw0rd!"

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

var (
	project     = envOr("COMPOSE_PROJECT", "agentbox")
	consoleBase = strings.TrimRight(envOr("CONSOLE_URL", "http://console.agentbox.corp:18080"), "/")
	sampleRepo  = envOr("SAMPLE_REPO", "git.agentbox.corp/samples/calc")
	perfStrict  = os.Getenv("PERF_STRICT") == "1"
	msgStrict   = os.Getenv("STRICT_MESSAGES") == "1"
)

// ---------- docker access ----------

type Result struct {
	Out  string
	Code int
	Dur  time.Duration
}

func run(timeout time.Duration, stdin string, name string, args ...string) Result {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	start := time.Now()
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
			buf.WriteString(err.Error())
		}
	}
	if ctx.Err() == context.DeadlineExceeded {
		code = 124
	}
	return Result{Out: strings.TrimSpace(buf.String()), Code: code, Dur: time.Since(start)}
}

func docker(args ...string) Result { return run(60*time.Second, "", "docker", args...) }

var (
	cidMu sync.Mutex
	cids  = map[string]string{}
)

func cidOf(svc string) (string, bool) {
	cidMu.Lock()
	defer cidMu.Unlock()
	if c, ok := cids[svc]; ok {
		return c, true
	}
	r := docker("ps", "-aq", "--filter", "label=com.docker.compose.project="+project, "--filter", "label=com.docker.compose.service="+svc)
	f := strings.Fields(r.Out)
	if r.Code != 0 || len(f) == 0 {
		return "", false
	}
	cids[svc] = f[0]
	return f[0], true
}

func hasSvc(svc string) bool { _, ok := cidOf(svc); return ok }

func cid(t testing.TB, svc string) string {
	t.Helper()
	c, ok := cidOf(svc)
	if !ok {
		t.Fatalf("no container for service %q in compose project %q: is the stack up? (scripts/test.sh brings up everything, including the tamper and dev profiles when needed)", svc, project)
	}
	return c
}

func running(svc string) bool {
	c, ok := cidOf(svc)
	if !ok {
		return false
	}
	return docker("inspect", "-f", "{{.State.Running}}", c).Out == "true"
}

// dexec runs a command in a service container. stdin, when set, is piped in.
func dexec(t testing.TB, svc string, timeout time.Duration, stdin string, args ...string) Result {
	t.Helper()
	a := []string{"exec"}
	if stdin != "" {
		a = append(a, "-i")
	}
	a = append(a, cid(t, svc))
	return run(timeout, stdin, "docker", append(a, args...)...)
}

func sh(t testing.TB, svc, script string) Result {
	t.Helper()
	return dexec(t, svc, 90*time.Second, "", "sh", "-c", script)
}

func dockerEnv(t testing.TB, svc string) map[string]string {
	t.Helper()
	r := docker("inspect", "-f", "{{json .Config.Env}}", cid(t, svc))
	var l []string
	if err := json.Unmarshal([]byte(r.Out), &l); err != nil {
		t.Fatalf("inspect %s env: %v: %s", svc, err, r.Out)
	}
	m := map[string]string{}
	for _, kv := range l {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

// ---------- polling ----------

func waitFor(t testing.TB, d time.Duration, what string, f func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(d)
	last := ""
	for {
		ok, info := f()
		if ok {
			return
		}
		last = info
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s; last state: %s", d, what, last)
		}
		time.Sleep(time.Second)
	}
}

func perf(t testing.TB, name string, got, limit float64, unit string) {
	t.Helper()
	t.Logf("PERF %s = %.1f %s (target < %.0f %s)", name, got, unit, limit, unit)
	if got > limit {
		if perfStrict {
			t.Errorf("%s %.1f %s exceeds %.0f %s", name, got, unit, limit, unit)
		} else {
			t.Logf("WARN %s over target; set PERF_STRICT=1 to fail on it", name)
		}
	}
}

func p95(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64{}, v...)
	sort.Float64s(s)
	i := int(float64(len(s))*0.95+0.5) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(s) {
		i = len(s) - 1
	}
	return s[i]
}

// ---------- ClickHouse and Postgres ----------

type chConf struct{ url, user, pass string }

var (
	chOnce sync.Once
	chc    chConf
)

func chConfig(t testing.TB) chConf {
	chOnce.Do(func() {
		chc = chConf{os.Getenv("CH_URL"), os.Getenv("CH_USER"), os.Getenv("CH_PASSWORD")}
		if chc.url == "" {
			e := dockerEnv(t, "correlator") // the correlator already holds the credentials
			chc = chConf{e["CLICKHOUSE_URL"], e["CLICKHOUSE_USER"], e["CLICKHOUSE_PASSWORD"]}
		}
	})
	return chc
}

const (
	evExpr   = "coalesce(nullIf(LogAttributes['event.name'],''), Body)"
	sbExpr   = "coalesce(nullIf(LogAttributes['agentbox.sandbox'],''), ResourceAttributes['agentbox.sandbox'])"
	userExpr = "coalesce(nullIf(ResourceAttributes['agentbox.user'],''), LogAttributes['agentbox.user'])"
	devExpr  = "coalesce(nullIf(ResourceAttributes['agentbox.device'],''), LogAttributes['agentbox.device'])"
)

func chQuery(t testing.TB, q string, params map[string]string) []map[string]any {
	t.Helper()
	c := chConfig(t)
	v := url.Values{"database": {"otel"}}
	for k, p := range params {
		v.Set("param_"+k, p)
	}
	req, _ := http.NewRequest("POST", strings.TrimRight(c.url, "/")+"/?"+v.Encode(), strings.NewReader(q+" FORMAT JSONEachRow"))
	if c.user != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("clickhouse: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("clickhouse %s: %s\nquery: %s", resp.Status, data, q)
	}
	var out []map[string]any
	for _, l := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(l)) == 0 {
			continue
		}
		var m map[string]any
		if json.Unmarshal(l, &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func num(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	}
	return 0
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func sub(m map[string]any, k string) map[string]any {
	s, _ := m[k].(map[string]any)
	if s == nil {
		return map[string]any{}
	}
	return s
}

// events returns timeline rows from ClickHouse. where is appended to the base
// filter (after "AND"); since is the lower time bound.
func events(t testing.TB, since time.Time, where string, params map[string]string) []map[string]any {
	t.Helper()
	if params == nil {
		params = map[string]string{}
	}
	params["since"] = since.UTC().Add(-2 * time.Second).Format(time.RFC3339Nano)
	q := fmt.Sprintf(`SELECT toString(Timestamp) AS ts, %s AS event, %s AS sandbox, %s AS user, %s AS device, LogAttributes AS attrs, ResourceAttributes AS res
		FROM otel_logs WHERE Timestamp >= parseDateTime64BestEffort({since:String}, 9) AND %s ORDER BY Timestamp`, evExpr, sbExpr, userExpr, devExpr, where)
	return chQuery(t, q, params)
}

func countEvents(t testing.TB, since time.Time, event, sandbox string) int {
	t.Helper()
	rows := events(t, since, evExpr+" = {ev:String} AND "+sbExpr+" = {sb:String}", map[string]string{"ev": event, "sb": sandbox})
	return len(rows)
}

func psql(t testing.TB, which, sql string) [][]string {
	t.Helper()
	var u string
	if which == "litellm" {
		u = dockerEnv(t, "gatekeeper")["LITELLM_DATABASE_URL"]
	} else {
		u = dockerEnv(t, "permsvc")["DATABASE_URL"]
	}
	if u == "" {
		t.Fatalf("no database URL for %s in the service environment", which)
	}
	r := dexec(t, "postgres", 30*time.Second, "", "psql", u, "-tAX", "-F", "\x1f", "-c", sql)
	if r.Code != 0 {
		t.Fatalf("psql (%s): %s\nsql: %s", which, r.Out, sql)
	}
	var rows [][]string
	for _, l := range strings.Split(r.Out, "\n") {
		if strings.TrimSpace(l) != "" {
			rows = append(rows, strings.Split(l, "\x1f"))
		}
	}
	return rows
}

// ---------- console API (cookie session via the real Keycloak login) ----------

type console struct {
	c    *http.Client
	user string
}

var (
	conMu   sync.Mutex
	consoles = map[string]*console{}
	formRe  = regexp.MustCompile(`<form[^>]*action="([^"]+)"`)
)

func consoleAs(t testing.TB, user string) *console {
	t.Helper()
	conMu.Lock()
	defer conMu.Unlock()
	if c, ok := consoles[user]; ok {
		return c
	}
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	resp, err := c.Get(consoleBase + "/auth/login")
	if err != nil {
		t.Fatalf("console login (%s): %v", user, err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := formRe.FindSubmatch(page)
	if m == nil {
		t.Fatalf("console login: no Keycloak login form at %s", resp.Request.URL)
	}
	action := html.UnescapeString(string(m[1]))
	resp, err = c.PostForm(action, url.Values{"username": {user}, "password": {password}, "credentialId": {""}})
	if err != nil {
		t.Fatalf("keycloak login (%s): %v", user, err)
	}
	resp.Body.Close()
	u, _ := url.Parse(consoleBase)
	ok := false
	for _, ck := range jar.Cookies(u) {
		ok = ok || ck.Name == "abx_session"
	}
	if !ok {
		t.Fatalf("console login for %s did not produce a session (status %s, final URL %s)", user, resp.Status, resp.Request.URL)
	}
	cn := &console{c: c, user: user}
	consoles[user] = cn
	return cn
}

func (c *console) do(method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, consoleBase+path, rd)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data, nil
}

func (c *console) get(t testing.TB, path string, out any) {
	t.Helper()
	code, data, err := c.do("GET", path, nil)
	if err != nil || code >= 300 {
		t.Fatalf("GET %s as %s: %d %v %s", path, c.user, code, err, data)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("GET %s: bad JSON: %v: %s", path, err, data)
	}
}

func (c *console) list(t testing.TB, path string) []map[string]any {
	t.Helper()
	var l []map[string]any
	c.get(t, path, &l)
	return l
}

func (c *console) post(t testing.TB, method, path string, body any) (int, string) {
	t.Helper()
	code, data, err := c.do(method, path, body)
	if err != nil {
		t.Fatalf("%s %s as %s: %v", method, path, c.user, err)
	}
	return code, string(data)
}

func (c *console) device(t testing.TB, id string) map[string]any {
	t.Helper()
	for _, d := range c.list(t, "/api/devices") {
		if str(d["device_id"]) == id {
			return d
		}
	}
	return nil
}

func (c *console) alerts(t testing.TB, detection, device, sandbox string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, a := range c.list(t, "/api/alerts") {
		if (detection == "" || str(a["detection"]) == detection) && (device == "" || str(a["device_id"]) == device) && (sandbox == "" || str(a["sandbox"]) == sandbox) {
			out = append(out, a)
		}
	}
	return out
}

// ---------- PC, CLI and sandboxes ----------

func cli(t testing.TB, pc string, args ...string) Result {
	t.Helper()
	return dexec(t, pc, 180*time.Second, "", append([]string{"agentbox"}, args...)...)
}

func enroll(t testing.TB, pc, user string) {
	t.Helper()
	r := dexec(t, pc, 120*time.Second, password, "agentbox", "enroll", "--user", user, "--password-stdin")
	if r.Code != 0 {
		t.Fatalf("agentbox enroll on %s as %s failed (%d): %s", pc, user, r.Code, r.Out)
	}
}

// ensureEnrolled enrolls pc as user unless the console already shows it
// attested and not revoked, then waits for the appliance to hold a bundle.
func ensureEnrolled(t testing.TB, pc, user string) {
	t.Helper()
	cn := consoleAs(t, "bob")
	d := cn.device(t, pc)
	if d == nil || str(d["attestation_status"]) != "pass" || d["revoked"] == true {
		enroll(t, pc, user)
	}
	waitFor(t, 120*time.Second, pc+" attested with a policy bundle", func() (bool, string) {
		r := cli(t, pc, "status")
		return strings.Contains(r.Out, "attestation pass") && strings.Contains(r.Out, "policy      bundle v"), r.Out
	})
}

type sbInfo struct{ PC, Name, ID, Image, State, Note string }

func listSandboxes(t testing.TB, pc string) []sbInfo {
	t.Helper()
	r := cli(t, pc, "list")
	if r.Code != 0 {
		t.Fatalf("agentbox list on %s: %s", pc, r.Out)
	}
	var out []sbInfo
	for i, l := range strings.Split(r.Out, "\n") {
		f := strings.Fields(l)
		if i == 0 || len(f) < 4 {
			continue
		}
		out = append(out, sbInfo{PC: pc, Name: f[0], ID: f[1], Image: f[2], State: f[3], Note: strings.Join(f[4:], " ")})
	}
	return out
}

func findSandbox(t testing.TB, pc, name string) *sbInfo {
	for _, s := range listSandboxes(t, pc) {
		if s.Name == name {
			s := s
			return &s
		}
	}
	return nil
}

const calcPy = "def add(a, b):\n    return a + b\n\n\ndef mul(a, b):\n    return a * b\n"

// ensureSandbox returns a running sandbox, creating it from image if needed.
func ensureSandbox(t testing.TB, pc, name, image string) sbInfo {
	t.Helper()
	s := findSandbox(t, pc, name)
	if s == nil {
		r := cli(t, pc, "new", sampleRepo, "--name", name, "--image", image)
		if r.Code != 0 {
			t.Fatalf("agentbox new %s (%s) on %s: %s", name, image, pc, r.Out)
		}
	} else if s.State != "running" {
		if r := cli(t, pc, "start", name); r.Code != 0 {
			t.Fatalf("agentbox start %s: %s", name, r.Out)
		}
	}
	var got sbInfo
	waitFor(t, 90*time.Second, "sandbox "+name+" running with ssh", func() (bool, string) {
		s := findSandbox(t, pc, name)
		if s == nil || s.State != "running" {
			return false, fmt.Sprint(s)
		}
		got = *s
		r := sbRun(t, got, 30*time.Second, "true")
		return r.Code == 0, r.Out
	})
	// The sample repository may not be reachable in every test bed: make sure
	// the project the scripted model works on exists.
	r := sbRun(t, got, 30*time.Second, "[ -f /workspace/calc.py ] || printf '"+strings.ReplaceAll(calcPy, "\n", "\\n")+"' > /workspace/calc.py; ls /workspace")
	if r.Code != 0 {
		t.Fatalf("seeding workspace in %s: %s", name, r.Out)
	}
	return got
}

const prelude = "set -a; [ -r /etc/agentbox/env ] && . /etc/agentbox/env; set +a; cd /workspace 2>/dev/null\n"

// sbRun executes a shell script inside a sandbox over ssh (ProxyCommand through
// agentbox-host). The script is shipped base64-encoded so its own stdin stays
// free for the programs it runs.
func sbRun(t testing.TB, s sbInfo, timeout time.Duration, script string) Result {
	t.Helper()
	b := base64.StdEncoding.EncodeToString([]byte(prelude + script + "\n"))
	cmd := fmt.Sprintf("echo %s | base64 -d > /tmp/t.$$.sh && sh /tmp/t.$$.sh </dev/null; rc=$?; rm -f /tmp/t.$$.sh; exit $rc", b)
	return dexec(t, s.PC, timeout, "", "agentbox", "shell", s.Name, "--", cmd)
}

const pyModelCall = `python3 - <<'PY'
import json, os, sys, time, urllib.request, urllib.error
def call(model, token=None):
    req = urllib.request.Request("http://llm.agentbox:4000/v1/chat/completions",
        data=json.dumps({"model": model, "messages": [{"role": "user", "content": "ping"}]}).encode(),
        headers={"Content-Type": "application/json", "Authorization": "Bearer " + (token or os.environ.get("TOKEN") or os.environ["AGENTBOX_TOKEN"])})
    t = time.time()
    try:
        r = urllib.request.urlopen(req, timeout=40); code, body = r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        code, body = e.code, e.read().decode()
    except Exception as e:
        code, body = 0, str(e)
    return code, body, (time.time() - t) * 1000
n = int(os.environ.get("N", "1")); out = []
for _ in range(n):
    code, body, ms = call(os.environ.get("MODEL", "deepseek-v3"))
    out.append({"code": code, "body": body[:400], "ms": ms})
print("RESULT " + json.dumps(out))
PY`

type mc struct {
	Code int     `json:"code"`
	Body string  `json:"body"`
	Ms   float64 `json:"ms"`
}

// modelCalls makes n chat-completion calls from inside the sandbox. token,
// when set, replaces the sandbox's own token (replay tests).
func modelCalls(t testing.TB, s sbInfo, model string, n int, token string) []mc {
	t.Helper()
	r := sbRun(t, s, 10*time.Minute, fmt.Sprintf("export N=%d MODEL=%s TOKEN=%s\n%s", n, model, token, pyModelCall))
	i := strings.LastIndex(r.Out, "RESULT ")
	if i < 0 {
		t.Fatalf("model call script gave no result (%d): %s", r.Code, r.Out)
	}
	var out []mc
	if err := json.Unmarshal([]byte(r.Out[i+7:]), &out); err != nil {
		t.Fatalf("model call result: %v: %s", err, r.Out)
	}
	return out
}

func modelCall(t testing.TB, s sbInfo, model string) mc { return modelCalls(t, s, model, 1, "")[0] }

// runTask runs a harness non-interactively in the sandbox and returns when it ends.
func runTask(t testing.TB, s sbInfo, harness, task string) (Result, time.Time) {
	t0 := time.Now()
	var cmd string
	switch harness {
	case "codex":
		cmd = fmt.Sprintf("codex exec --skip-git-repo-check %q", task)
	case "opencode":
		cmd = fmt.Sprintf("opencode run %q", task)
	default:
		t.Fatalf("unknown harness %s", harness)
	}
	return sbRun(t, s, 5*time.Minute, cmd), t0
}

const unitTestTask = "Add a unit test for calc.mul and run the unit test suite"

func suitePasses(t testing.TB, s sbInfo) Result {
	return sbRun(t, s, 2*time.Minute, "cd /workspace && ls test_agent_added.py && python3 -m unittest discover -p 'test*.py'")
}

// forgetConsoleSessions drops cached console logins. The console keeps
// sessions in memory, so after a restart administrators sign in again.
func forgetConsoleSessions() {
	conMu.Lock()
	defer conMu.Unlock()
	consoles = map[string]*console{}
}

// waitConsoleHealthy waits for the console process (no login needed).
func waitConsoleHealthy(t *testing.T, d time.Duration) {
	t.Helper()
	waitFor(t, d, "the console to answer /healthz", func() (bool, string) {
		resp, err := http.Get(consoleBase + "/healthz")
		if err != nil {
			return false, err.Error()
		}
		resp.Body.Close()
		return resp.StatusCode == 200, resp.Status
	})
	forgetConsoleSessions()
}
