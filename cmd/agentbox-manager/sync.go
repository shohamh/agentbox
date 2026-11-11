package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"

	"agentbox/pkg/bundle"
)

type command struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	Args      json.RawMessage `json:"args"`
	CreatedBy string          `json:"created_by"`
	Reason    string          `json:"reason"`
}

// syncLoop long-polls permsvc for newer signed bundles and admin commands.
func (m *Manager) syncLoop() {
	backoff := time.Second
	for {
		m.mu.RLock()
		ready := m.st.DeviceID != "" && m.attest.Status == "pass"
		have := int64(0)
		if m.bundle != nil {
			have = m.bundle.Version
		}
		m.mu.RUnlock()
		if !ready {
			select {
			case <-m.wake:
			case <-time.After(5 * time.Second):
			}
			continue
		}
		err := m.syncOnce(have)
		if err != nil {
			log.Printf("sync: %v", err)
			m.setReach(err)
			time.Sleep(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		m.setReach(nil)
		backoff = time.Second
	}
}

func (m *Manager) syncOnce(have int64) error {
	c := m.mtlsClient(40 * time.Second)
	resp, err := c.Get(fmt.Sprintf("%s/v1/sync?have=%d&wait=25", m.cfg.PermsvcURL, have))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 403 {
		// Revoked: keep sandboxes and the last bundle; re-attestation will fail too.
		m.mu.Lock()
		m.attest.Status, m.attest.Reason = "fail", string(bytes.TrimSpace(data))
		m.mu.Unlock()
		time.Sleep(10 * time.Second)
		return fmt.Errorf("refused: %s", bytes.TrimSpace(data))
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", resp.Status, bytes.TrimSpace(data))
	}
	var out struct {
		Bundle   *bundle.Signed `json:"bundle"`
		Commands []command      `json:"commands"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	if out.Bundle != nil {
		m.applyBundle(out.Bundle)
	}
	for _, c := range out.Commands {
		res := m.runCommand(c)
		m.ack(c.ID, res)
	}
	return nil
}

// applyBundle verifies signature, device, version and expiry, then applies.
func (m *Manager) applyBundle(s *bundle.Signed) {
	m.mu.RLock()
	dev := m.st.DeviceID
	cur := int64(0)
	if m.bundle != nil {
		cur = m.bundle.Version
	}
	m.mu.RUnlock()
	b, err := bundle.Verify(s, m.pub, dev, cur, time.Now())
	if err != nil {
		log.Printf("bundle rejected: %v", err)
		m.ev.EmitSev("WARN", "bundle.rejected", map[string]any{"error": err.Error()})
		return
	}
	m.mu.Lock()
	old := m.bundle
	m.bundle = b
	m.st.Bundle = s
	m.mu.Unlock()
	m.saveState()
	log.Printf("bundle v%d applied: models=%v egress=%v images=%d", b.Version, b.Models, b.Egress, len(b.Images))
	m.emit("bundle.applied", map[string]any{"agentbox.bundle_version": b.Version, "egress": b.Egress, "models": b.Models})
	if old != nil && old.User != b.User {
		log.Printf("bundle user changed %s -> %s", old.User, b.User)
	}
	go m.heartbeat()
}

func (m *Manager) runCommand(c command) string {
	var args struct {
		Sandbox string `json:"sandbox"`
	}
	json.Unmarshal(c.Args, &args)
	who := fmt.Sprintf("%s by %s: %s", map[string]string{"stop_sandbox": "stopped", "recycle_sandbox": "recycled", "recycle_appliance": "appliance recycled"}[c.Kind], c.CreatedBy, c.Reason)
	log.Printf("command %d %s %s (%s)", c.ID, c.Kind, args.Sandbox, who)
	switch c.Kind {
	case "stop_sandbox":
		if err := m.stopSandbox(args.Sandbox, who, c.CreatedBy); err != nil {
			return "error: " + err.Error()
		}
		return "stopped"
	case "recycle_sandbox":
		if err := m.stopSandbox(args.Sandbox, who, c.CreatedBy); err != nil {
			return "error: " + err.Error()
		}
		if err := m.startSandbox(args.Sandbox); err != nil {
			return "error: " + err.Error()
		}
		return "recycled"
	case "recycle_appliance":
		go func() {
			time.Sleep(time.Second)
			m.recycleAppliance(who)
		}()
		return "recycling"
	}
	return "unknown command"
}

func (m *Manager) ack(id int64, result string) {
	b, _ := json.Marshal(map[string]string{"result": result})
	c := m.mtlsClient(10 * time.Second)
	resp, err := c.Post(fmt.Sprintf("%s/v1/commands/%d/ack", m.cfg.PermsvcURL, id), "application/json", bytes.NewReader(b))
	if err == nil {
		resp.Body.Close()
	}
}

// recycleAppliance stops sandboxes cleanly and "reboots": the process exits
// and the hypervisor (here the container runtime) restarts the appliance from
// its read-only base; sandboxes marked running come back from saved state.
func (m *Manager) recycleAppliance(reason string) {
	m.emit("appliance.recycle", map[string]any{"reason": reason})
	m.stopAll(reason, true)
	time.Sleep(1500 * time.Millisecond)
	// In the VM (systemd is PID 1) a recycle is a reboot from the signed base
	// disk; in the container test bed the process exits and the runtime restarts it.
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		if exec.Command("systemctl", "reboot").Run() == nil {
			time.Sleep(time.Minute)
		}
	}
	os.Exit(0)
}

func (m *Manager) heartbeatLoop() {
	for {
		m.heartbeat()
		time.Sleep(10 * time.Second)
	}
}

func (m *Manager) heartbeat() {
	m.mu.RLock()
	ready := m.st.DeviceID != "" && m.attest.Status == "pass"
	ver := int64(0)
	if m.bundle != nil {
		ver = m.bundle.Version
	}
	m.mu.RUnlock()
	if !ready {
		return
	}
	st := map[string]any{"bundle_version": ver, "appliance_version": m.rel.Version, "flavor": m.rel.Flavor, "sandboxes": m.sandboxList()}
	b, _ := json.Marshal(st)
	c := m.mtlsClient(10 * time.Second)
	resp, err := c.Post(m.cfg.PermsvcURL+"/v1/status", "application/json", bytes.NewReader(b))
	if err != nil {
		m.setReach(err)
		return
	}
	resp.Body.Close()
}

// permsvc forwards a call for the CLI (permission requests).
func (m *Manager) permsvc(method, path string, body []byte) (int, []byte, error) {
	c := m.mtlsClient(70 * time.Second)
	req, _ := http.NewRequest(method, m.cfg.PermsvcURL+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data, nil
}
