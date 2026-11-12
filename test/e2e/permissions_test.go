//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Permission management, end to end: a request from the CLI, the class the
// policy gives it, the approver's decision in the console, the signed bundle that
// reaches the appliance, and the thing it enforces.
//
//   E2E-21  image:    a group that lacks an image; request, approve, sandbox of that image starts
//   E2E-22  model:    a model outside the policy is refused; approved, it answers; rolled back, refused again
//   E2E-23  port:     raw TCP forwarding needs the port permission; denied ports; data flows once granted
//   E2E-24  tier:     a granted tier is enforced against the device's assurance level at the gateway
//   E2E-25  mount:    refused with an honest message (no folder sync in this version)
//   E2E-26  expiry and renewal: a time-boxed grant ends within seconds; a flagged one renews with one command
//   E2E-27  an approver's rule turns a pattern into automatic grants for other users

func permReason(what string) string { return fmt.Sprintf("e2e %s %d", what, time.Now().UnixNano()%1e9) }

// allowPending files a request without waiting for a decision.
func allowPending(t *testing.T, pc, perm, reason string) Result {
	t.Helper()
	return cli(t, pc, "allow", perm, "--reason", reason, "--wait", "0")
}

// requestRow finds a request in the console by its reason text.
func requestRow(t *testing.T, cn *console, reason string) map[string]any {
	t.Helper()
	var row map[string]any
	waitFor(t, 20*time.Second, "request "+reason+" in the console", func() (bool, string) {
		for _, r := range cn.list(t, "/api/requests") {
			if str(r["reason"]) == reason {
				row = r
				return true, ""
			}
		}
		return false, "not listed"
	})
	return row
}

func decide(t *testing.T, cn *console, row map[string]any, action string, body map[string]any) {
	t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	if _, ok := body["reason"]; !ok {
		body["reason"] = "E2E decision"
	}
	if code, out := cn.post(t, "POST", fmt.Sprintf("/api/requests/%d/%s", num(row["id"]), action), body); code >= 300 {
		t.Fatalf("%s request %v as %s: %d %s", action, row["id"], cn.user, code, out)
	}
}

// rollbackGrant removes a grant (cleanup, and the revoke path).
func rollbackGrant(t *testing.T, perm, user string) {
	t.Helper()
	cn := consoleAs(t, "bob")
	for _, r := range cn.list(t, "/api/requests") {
		if str(r["permission"]) == perm && str(r["username"]) == user && (str(r["status"]) == "active" || str(r["status"]) == "approved") {
			cn.post(t, "POST", fmt.Sprintf("/api/requests/%d/rollback", num(r["id"])), map[string]any{"reason": "E2E cleanup"})
		}
	}
}

func egressBlocked(t *testing.T, s sbInfo, host string) bool {
	t.Helper()
	r := sbRun(t, s, 60*time.Second, fmt.Sprintf(egressScript, host))
	return strings.Contains(r.Out, "CODE 403") && strings.Contains(r.Out, "egress to")
}

// ---------- E2E-21: image ----------

func TestE2E_21_ImagePermission(t *testing.T) {
	ensureEnrolled(t, pc1, user1)
	orig := psql(t, "agentbox", `SELECT policy->'images' FROM groups WHERE name='default'`)
	if len(orig) == 0 {
		t.Skip("no default group")
	}
	// The group no longer lists the omp image; the catalog still has it.
	psql(t, "agentbox", `UPDATE groups SET policy = jsonb_set(policy,'{images}', (policy->'images') - 'omp'), updated_by='e2e-21' WHERE name='default'`)
	psql(t, "agentbox", `SELECT pg_notify('abx_policy','*')`)
	t.Cleanup(func() {
		psql(t, "agentbox", fmt.Sprintf(`UPDATE groups SET policy = jsonb_set(policy,'{images}', '%s'::jsonb), updated_by='e2e-21' WHERE name='default'`, orig[0][0]))
		psql(t, "agentbox", `SELECT pg_notify('abx_policy','*')`)
		cli(t, pc1, "rm", "e2e-perm-img")
		rollbackGrant(t, "image:omp", user1)
	})
	waitFor(t, 30*time.Second, "the image to leave the sandbox policy", func() (bool, string) {
		r := cli(t, pc1, "new", "--name", "e2e-perm-img", "--image", "omp")
		if r.Code != 0 && strings.Contains(r.Out, "not in your policy") {
			return true, ""
		}
		cli(t, pc1, "rm", "e2e-perm-img")
		return false, r.Out
	})

	reason := permReason("image")
	r := allowPending(t, pc1, "image:omp", reason)
	if r.Code != 0 || !strings.Contains(r.Out, "pending") || !strings.Contains(r.Out, "approval") {
		t.Fatalf("image outside the group must wait for an approver: %d %s", r.Code, r.Out)
	}
	carol := consoleAs(t, "carol")
	row := requestRow(t, carol, reason)
	if str(row["class"]) != "approval" || str(row["status"]) != "pending" {
		t.Fatalf("request class/status = %v/%v, want approval/pending", row["class"], row["status"])
	}
	decide(t, carol, row, "approve", nil)
	t0 := time.Now()
	waitFor(t, 30*time.Second, "the approved image to be usable", func() (bool, string) {
		r := cli(t, pc1, "new", "--name", "e2e-perm-img", "--image", "omp")
		return r.Code == 0, r.Out
	})
	t.Logf("PERF approved image usable %s after the approval", time.Since(t0).Round(100*time.Millisecond))

	// an image that is not in the catalog can never be requested
	if n := allowPending(t, pc1, "image:no-such-image", permReason("image never")); n.Code == 0 || !strings.Contains(n.Out, "never") {
		t.Errorf("an unknown image must be refused outright: %d %s", n.Code, n.Out)
	}
}

// ---------- E2E-22: model ----------

func TestE2E_22_ModelPermission(t *testing.T) {
	ensureEnrolled(t, pc1, user1)
	s := ensureSandbox(t, pc1, "e2e-codex", "codex")
	t.Cleanup(func() { rollbackGrant(t, "model:small", user1) })
	rollbackGrant(t, "model:small", user1) // a grant left by an earlier run
	waitFor(t, 40*time.Second, "model small to be refused", func() (bool, string) {
		c := modelCall(t, s, "small")
		return c.Code == 403, fmt.Sprintf("%d %s", c.Code, c.Body)
	})
	if c := modelCall(t, s, "small"); !strings.Contains(c.Body, "not in your policy") {
		t.Errorf("refusal does not say why: %s", c.Body)
	}

	reason := permReason("model")
	if r := allowPending(t, pc1, "model:small", reason); r.Code != 0 || !strings.Contains(r.Out, "pending") {
		t.Fatalf("a model outside the group must wait for an approver: %d %s", r.Code, r.Out)
	}
	carol := consoleAs(t, "carol")
	decide(t, carol, requestRow(t, carol, reason), "approve", nil)
	t0 := time.Now()
	waitFor(t, 60*time.Second, "the approved model to answer", func() (bool, string) {
		c := modelCall(t, s, "small")
		return c.Code == 200, fmt.Sprintf("%d %s", c.Code, c.Body)
	})
	t.Logf("PERF approved model answering %s after the approval", time.Since(t0).Round(100*time.Millisecond))

	rollbackGrant(t, "model:small", user1)
	waitFor(t, 60*time.Second, "the model to be refused again after the rollback", func() (bool, string) {
		c := modelCall(t, s, "small")
		return c.Code == 403, fmt.Sprintf("%d %s", c.Code, c.Body)
	})

	if n := allowPending(t, pc1, "model:no-such-model", permReason("model never")); n.Code == 0 || !strings.Contains(n.Out, "never") {
		t.Errorf("a model the platform does not serve must be refused outright: %d %s", n.Code, n.Out)
	}
}

// ---------- E2E-23: port ----------

func TestE2E_23_PortPermission(t *testing.T) {
	ensureEnrolled(t, pc1, user1)
	s := ensureSandbox(t, pc1, "e2e-codex", "codex")
	t.Cleanup(func() { rollbackGrant(t, "port:8080", user1) })
	rollbackGrant(t, "port:8080", user1)
	waitFor(t, 30*time.Second, "forward to be refused without the permission", func() (bool, string) {
		r := cli(t, pc1, "forward", s.Name, "8080", "--listen", "127.0.0.1:0")
		return r.Code != 0 && strings.Contains(r.Out, "allow port:8080"), r.Out
	})
	for _, p := range []string{"4000", "3128", "53"} {
		if r := allowPending(t, pc1, "port:"+p, permReason("port never")); r.Code == 0 || !strings.Contains(r.Out, "never") {
			t.Errorf("port %s (the model endpoint, the proxy, DNS) must never be allowed: %d %s", p, r.Code, r.Out)
		}
	}
	r := allowPending(t, pc1, "port:8080", permReason("port"))
	if r.Code != 0 || !strings.Contains(r.Out, "flagged") || !strings.Contains(r.Out, "active") {
		t.Fatalf("a port is applied at once and flagged for review: %d %s", r.Code, r.Out)
	}
	if isNative(pc1) {
		t.Log("native PC: the live data check needs a POSIX shell on the PC and runs in the container bed")
		return
	}
	sbRun(t, s, 30*time.Second, `mkdir -p /tmp/www && echo port-forward-ok > /tmp/www/index.html && (cd /tmp/www && nohup python3 -m http.server 8080 >/dev/null 2>&1 &) ; sleep 1; true`)
	waitFor(t, 40*time.Second, "data to flow through the forwarded port", func() (bool, string) {
		out := sh(t, pc1, fmt.Sprintf(`agentbox forward %s 8080 --listen 127.0.0.1:18081 >/tmp/fwd.log 2>&1 &
P=$!; sleep 2; curl -s --max-time 5 http://127.0.0.1:18081/; kill $P 2>/dev/null; cat /tmp/fwd.log`, s.Name))
		return strings.Contains(out.Out, "port-forward-ok"), out.Out
	})
}

// ---------- E2E-24: tier ----------

func TestE2E_24_TierPermission(t *testing.T) {
	ensureEnrolled(t, pc1, user1)
	s := ensureSandbox(t, pc1, "e2e-codex", "codex")
	t.Cleanup(func() { rollbackGrant(t, "tier:3", user1); rollbackGrant(t, "tier:2", user1) })
	rollbackGrant(t, "tier:3", user1)
	rollbackGrant(t, "tier:2", user1)
	if c := modelCall(t, s, "deepseek-v3"); c.Code != 200 {
		t.Fatalf("baseline model call: %d %s", c.Code, c.Body)
	}
	// tier 2 fits this PC's assurance level; tier 3 does not (it needs level B)
	carol := consoleAs(t, "carol")
	for _, tier := range []string{"2", "3"} {
		reason := permReason("tier " + tier)
		if r := allowPending(t, pc1, "tier:"+tier, reason); r.Code != 0 || !strings.Contains(r.Out, "pending") {
			t.Fatalf("tier %s must wait for an approver: %d %s", tier, r.Code, r.Out)
		}
		decide(t, carol, requestRow(t, carol, reason), "approve", nil)
		if tier == "2" {
			time.Sleep(8 * time.Second)
			if c := modelCall(t, s, "deepseek-v3"); c.Code != 200 {
				t.Fatalf("tier 2 is within level C but the model call failed: %d %s", c.Code, c.Body)
			}
		}
	}
	waitFor(t, 60*time.Second, "tier 3 to be refused at the gateway for this assurance level", func() (bool, string) {
		c := modelCall(t, s, "deepseek-v3")
		return c.Code == 403 && strings.Contains(c.Body, "tier 3"), fmt.Sprintf("%d %s", c.Code, c.Body)
	})
	rollbackGrant(t, "tier:3", user1)
	waitFor(t, 60*time.Second, "model access to return after the tier grant is rolled back", func() (bool, string) {
		c := modelCall(t, s, "deepseek-v3")
		return c.Code == 200, fmt.Sprintf("%d %s", c.Code, c.Body)
	})
	if n := allowPending(t, pc1, "tier:4", permReason("tier never")); n.Code == 0 || !strings.Contains(n.Out, "never") {
		t.Errorf("a tier above the group's cap must be refused outright: %d %s", n.Code, n.Out)
	}
}

// ---------- E2E-25: mount ----------

func TestE2E_25_MountIsRefusedHonestly(t *testing.T) {
	ensureEnrolled(t, pc1, user1)
	r := allowPending(t, pc1, "mount:/home/alice/src/payments", permReason("mount"))
	if r.Code == 0 || !strings.Contains(r.Out, "not available") {
		t.Fatalf("mount must be refused with an explanation, not granted and ignored: %d %s", r.Code, r.Out)
	}
	for _, row := range consoleAs(t, "bob").list(t, "/api/requests") {
		if strings.HasPrefix(str(row["permission"]), "mount:") && str(row["username"]) == user1 && (str(row["status"]) == "active" || str(row["status"]) == "approved") {
			t.Errorf("a mount grant exists although mounts do nothing: %v", row)
		}
	}
	// reason is mandatory for everything
	if x := cli(t, pc1, "allow", "egress:pypi.agentbox.corp"); x.Code == 0 || !strings.Contains(x.Out, "reason") {
		t.Errorf("a request without a reason must be refused: %d %s", x.Code, x.Out)
	}
}

// ---------- E2E-26: expiry and renewal ----------

func TestE2E_26_GrantExpiryAndRenewal(t *testing.T) {
	ensureEnrolled(t, pc1, user1)
	s := ensureSandbox(t, pc1, "e2e-codex", "codex")
	host := "expiry-test.agentbox.corp"
	t.Cleanup(func() { rollbackGrant(t, "egress:"+host, user1); rollbackGrant(t, "egress:x.lab.agentbox.corp", user1) })
	rollbackGrant(t, "egress:"+host, user1)
	if !egressBlocked(t, s, host) {
		t.Fatalf("%s must be blocked before the grant", host)
	}
	reason := permReason("expiry")
	if r := allowPending(t, pc1, "egress:"+host, reason); r.Code != 0 || !strings.Contains(r.Out, "pending") {
		t.Fatalf("an unlisted host waits for an approver: %d %s", r.Code, r.Out)
	}
	carol := consoleAs(t, "carol")
	row := requestRow(t, carol, reason)
	decide(t, carol, row, "approve", map[string]any{"expires_hours": 0.014}) // about 50 seconds
	var expires time.Time
	waitFor(t, 20*time.Second, "the approver's time box to be recorded", func() (bool, string) {
		for _, r := range carol.list(t, "/api/requests") {
			if num(r["id"]) == num(row["id"]) {
				tm, err := time.Parse(time.RFC3339, str(r["expires_at"]))
				if err == nil {
					expires = tm
					return true, ""
				}
			}
		}
		return false, "no expires_at"
	})
	waitFor(t, 30*time.Second, host+" to open", func() (bool, string) { return !egressBlocked(t, s, host), "still blocked" })
	waitFor(t, 3*time.Minute, host+" to close again when the time box ends", func() (bool, string) { return egressBlocked(t, s, host), "still open" })
	late := time.Since(expires)
	t.Logf("PERF the grant stopped applying %s after its expiry (limit 45s)", late.Round(time.Second))
	if late > 45*time.Second {
		t.Errorf("a time-boxed grant kept applying %s after it expired", late)
	}
	for _, r := range carol.list(t, "/api/requests") {
		if num(r["id"]) == num(row["id"]) && str(r["status"]) != "expired" {
			t.Errorf("the request is %v after expiry, want expired", r["status"])
		}
	}

	// A flagged grant is renewed by asking again, not duplicated.
	lab := "egress:x.lab.agentbox.corp"
	reasonF := permReason("flagged")
	first := allowPending(t, pc1, lab, reasonF)
	if first.Code != 0 || !strings.Contains(first.Out, "flagged") || !strings.Contains(first.Out, "14 days") {
		t.Fatalf("a lab host is applied at once and flagged for 14 days: %d %s", first.Code, first.Out)
	}
	bob := consoleAs(t, "bob")
	countGrants := func() (n int, exp string) {
		for _, r := range bob.list(t, "/api/requests") {
			if str(r["permission"]) == lab && str(r["username"]) == user1 && str(r["status"]) == "active" {
				n++
				exp = str(r["expires_at"])
			}
		}
		return
	}
	n1, exp1 := countGrants()
	time.Sleep(2 * time.Second)
	again := allowPending(t, pc1, lab, permReason("flagged renew"))
	if again.Code != 0 || !strings.Contains(again.Out, "renewed") {
		t.Errorf("asking again for a flagged grant must renew it: %d %s", again.Code, again.Out)
	}
	n2, exp2 := countGrants()
	if n2 != n1 || exp2 <= exp1 {
		t.Errorf("renewal must extend the existing grant, not add one: grants %d -> %d, expires %s -> %s", n1, n2, exp1, exp2)
	}
	// an automatic grant is just reported as already held
	if r1 := allowPending(t, pc1, "egress:pypi.agentbox.corp", permReason("auto")); r1.Code != 0 {
		t.Fatalf("automatic grant: %s", r1.Out)
	}
	if r2 := allowPending(t, pc1, "egress:pypi.agentbox.corp", permReason("auto again")); r2.Code != 0 || !strings.Contains(r2.Out, "already") {
		t.Errorf("asking again for an automatic grant must say it is already held: %d %s", r2.Code, r2.Out)
	}
}

// ---------- E2E-27: an approver's rule ----------

func TestE2E_27_ApproverRuleMakesFutureRequestsAutomatic(t *testing.T) {
	if !hasSvc(app2) || !running(app2) {
		t.Skip("needs the second PC (pc-2)")
	}
	ensureEnrolled(t, pc1, user1)
	ensureEnrolled(t, pc2, user2)
	perm := "egress:rule-me.agentbox.corp"
	t.Cleanup(func() {
		psql(t, "agentbox", `DELETE FROM auto_rules WHERE pattern='`+perm+`'`)
		rollbackGrant(t, perm, user1)
		rollbackGrant(t, perm, user2)
	})
	psql(t, "agentbox", `DELETE FROM auto_rules WHERE pattern='`+perm+`'`)
	rollbackGrant(t, perm, user1)
	rollbackGrant(t, perm, user2)
	reason := permReason("rule")
	if r := allowPending(t, pc1, perm, reason); r.Code != 0 || !strings.Contains(r.Out, "pending") {
		t.Fatalf("first request waits for an approver: %d %s", r.Code, r.Out)
	}
	carol := consoleAs(t, "carol")
	row := requestRow(t, carol, reason)
	// an approver cannot create rules: that is an admin decision
	if code, _ := carol.post(t, "POST", fmt.Sprintf("/api/requests/%d/rule", num(row["id"])), map[string]any{"pattern": perm}); code != 403 {
		t.Errorf("an approver created an automatic rule (status %d); only admins may", code)
	}
	bob := consoleAs(t, "bob")
	decide(t, bob, row, "rule", map[string]any{"pattern": perm})
	waitFor(t, 20*time.Second, "the same request from another user to be automatic", func() (bool, string) {
		r := allowPending(t, pc2, perm, permReason("rule follow-up"))
		return r.Code == 0 && strings.Contains(r.Out, "automatically"), r.Out
	})
}
