//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Degraded-mode checks from the plan's offline-operation table (O3):
// control plane, gateway, model server, console and telemetry each go down
// and come back; sandboxes keep running throughout.

func stopSvc(t *testing.T, svc string) {
	t.Helper()
	id := cid(t, svc)
	if r := docker("stop", "-t", "5", id); r.Code != 0 {
		t.Fatalf("docker stop %s: %s", svc, r.Out)
	}
	t.Cleanup(func() {
		if !running(svc) {
			docker("start", id)
		}
	})
}

func startSvc(t *testing.T, svc string) {
	t.Helper()
	if r := docker("start", cid(t, svc)); r.Code != 0 {
		t.Fatalf("docker start %s: %s", svc, r.Out)
	}
}

func sandboxAlive(t *testing.T, s sbInfo, phase string) {
	t.Helper()
	if st := findSandbox(t, s.PC, s.Name); st == nil || st.State != "running" {
		t.Fatalf("sandbox not running %s: %v", phase, st)
	}
	if r := cli(t, s.PC, "shell", s.Name, "--", "echo", "alive"); r.Code != 0 || !strings.Contains(r.Out, "alive") {
		t.Fatalf("sandbox shell broke %s: %s", phase, r.Out)
	}
}

func recoverModel(t *testing.T, s sbInfo, d time.Duration) {
	t.Helper()
	waitFor(t, d, "model calls to recover", func() (bool, string) {
		c := modelCall(t, s, "deepseek-v3")
		return c.Code == 200, fmt.Sprintf("%d %s", c.Code, c.Body)
	})
}

func degradedSandbox(t *testing.T) sbInfo {
	ensureEnrolled(t, pc1, user1)
	s := ensureSandbox(t, pc1, "e2e-codex", "codex")
	recoverModel(t, s, 60*time.Second)
	return s
}

func TestE2E_11_PermissionServiceDown(t *testing.T) {
	s := degradedSandbox(t)
	stopSvc(t, "permsvc")
	sandboxAlive(t, s, "with permsvc down")
	if c := modelCall(t, s, "deepseek-v3"); c.Code != 200 {
		t.Errorf("model calls must not depend on permsvc: %d %s", c.Code, c.Body)
	}
	r := cli(t, pc1, "allow", "egress:pypi.agentbox.corp", "--reason", "outage check", "--wait", "0")
	if r.Code == 0 || !strings.Contains(strings.ToLower(r.Out), "unreachable") {
		t.Errorf("agentbox allow should fail with a clear 'unreachable' message while permsvc is down: %d %s", r.Code, r.Out)
	}
	startSvc(t, "permsvc")
	waitFor(t, 120*time.Second, "agentbox allow to work again", func() (bool, string) {
		r := cli(t, pc1, "allow", "egress:pypi.agentbox.corp", "--reason", "recovered", "--wait", "0")
		return r.Code == 0, r.Out
	})
	sandboxAlive(t, s, "after permsvc recovery")
}

func TestE2E_12_GatewayDown(t *testing.T) {
	s := degradedSandbox(t)
	stopSvc(t, "gatekeeper")
	sandboxAlive(t, s, "with the gateway authorizer down")
	c := modelCall(t, s, "deepseek-v3")
	if c.Code == 200 {
		t.Fatalf("model call succeeded with gatekeeper stopped (fail-open!)")
	}
	t.Logf("with gatekeeper down: %d after %.0f ms: %s", c.Code, c.Ms, c.Body)
	clearMessage(t, c)
	startSvc(t, "gatekeeper")
	recoverModel(t, s, 120*time.Second)
}

func TestE2E_13_ModelServerDown(t *testing.T) {
	s := degradedSandbox(t)
	svc := "litellm"
	stopSvc(t, svc)
	sandboxAlive(t, s, "with LiteLLM down")
	c := modelCall(t, s, "deepseek-v3")
	if c.Code == 200 || strings.TrimSpace(c.Body) == "" {
		t.Fatalf("expected a clear error with LiteLLM down, got %d %q", c.Code, c.Body)
	}
	t.Logf("with LiteLLM down: %d: %s", c.Code, c.Body)
	// a harness shows the error instead of hanging
	r := sbRun(t, s, 3*time.Minute, `timeout 150 codex exec --skip-git-repo-check "hello"; echo "EXIT=$?"`)
	if strings.Contains(r.Out, "EXIT=124") {
		t.Errorf("codex hung while the model server was down:\n%s", r.Out)
	}
	startSvc(t, svc)
	recoverModel(t, s, 300*time.Second) // LiteLLM starts slowly
}

func TestE2E_14_ConsoleDown(t *testing.T) {
	s := degradedSandbox(t)
	stopSvc(t, "console")
	sandboxAlive(t, s, "with the console down")
	// enforcement continues: the console holds no runtime state the gateway needs
	for i := 0; i < 3; i++ {
		if c := modelCall(t, s, "deepseek-v3"); c.Code != 200 {
			t.Errorf("model call %d failed with the console down: %d %s", i, c.Code, c.Body)
		}
	}
	if r := cli(t, pc1, "allow", "egress:pypi.agentbox.corp", "--reason", "console down", "--wait", "0"); r.Code != 0 {
		t.Errorf("permission requests go to permsvc, not the console: %s", r.Out)
	}
	startSvc(t, "console")
	waitConsoleHealthy(t, 180*time.Second)
	waitFor(t, 120*time.Second, "the console API to answer again", func() (bool, string) {
		code, _, err := consoleAs(t, "bob").do("GET", "/api/overview", nil)
		return err == nil && code == 200, fmt.Sprint(code, err)
	})
}

func TestE2E_15_TelemetryTierDown(t *testing.T) {
	s := degradedSandbox(t)
	stopSvc(t, "otel-gateway")
	t0 := time.Now()
	sandboxAlive(t, s, "with the telemetry gateway down")
	if c := modelCall(t, s, "deepseek-v3"); c.Code != 200 {
		t.Errorf("model calls must not depend on telemetry: %d %s", c.Code, c.Body)
	}
	// appliance events produced during the outage are buffered and arrive afterwards
	if r := cli(t, pc1, "recycle", s.Name); r.Code != 0 {
		t.Fatalf("recycle during the outage: %s", r.Out)
	}
	startSvc(t, "otel-gateway")
	waitFor(t, 240*time.Second, "buffered sandbox.started/sandbox.stopped events to arrive after recovery", func() (bool, string) {
		a := countEvents(t, t0, "sandbox.stopped", s.ID)
		b := countEvents(t, t0, "sandbox.started", s.ID)
		return a >= 1 && b >= 1, fmt.Sprintf("stopped=%d started=%d", a, b)
	})
	ensureSandbox(t, pc1, s.Name, "codex")
}
