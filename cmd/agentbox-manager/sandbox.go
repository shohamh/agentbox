package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agentbox/pkg/bundle"
)

type Sandbox struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Image     string    `json:"image"`
	ImageRef  string    `json:"image_ref"`
	Digest    string    `json:"digest"`
	Harness   string    `json:"harness"`
	Repo      string    `json:"repo"`
	IP        string    `json:"ip"`
	Token     string    `json:"-"`
	TokenFile string    `json:"-"`
	SSHPub    string    `json:"-"`
	State     string    `json:"state"` // creating | running | stopped | failed
	Desired   string    `json:"desired"`
	Created   time.Time `json:"created"`
	Started   time.Time `json:"started,omitempty"`
	StopNote  string    `json:"stop_reason,omitempty"`
	StoppedBy string    `json:"stopped_by,omitempty"`
	Error     string    `json:"error,omitempty"`
	Bundle    int64     `json:"policy_version"`
	Limits    bundle.Limits `json:"limits"`
	// ModelEnv is the last model environment computed from a policy bundle. A
	// sandbox that restarts while no bundle is loaded (offline restore, re-enrollment)
	// keeps the model names it last had instead of starting without any.
	ModelEnv []string `json:"model_env,omitempty"`
}

// persisted form includes the token (data disk is encrypted in production).
type sandboxDisk struct {
	Sandbox
	Token  string `json:"token"`
	SSHPub string `json:"ssh_pub"`
}

func (m *Manager) sbDir(id string) string { return filepath.Join(m.cfg.DataDir, "sandboxes", id) }

func (m *Manager) saveSandbox(s *Sandbox) {
	b, _ := json.MarshalIndent(sandboxDisk{Sandbox: *s, Token: s.Token, SSHPub: s.SSHPub}, "", "  ")
	writeFileAtomic(filepath.Join(m.sbDir(s.ID), "meta.json"), b, 0o600)
}

func newID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return "sb-" + hex.EncodeToString(b)
}

func newToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return "abx-sbx-" + hex.EncodeToString(b)
}

type createReq struct {
	Name    string `json:"name"`
	Repo    string `json:"repo"`
	Image   string `json:"image"`
	Harness string `json:"harness"`
	SSHPub  string `json:"ssh_pub"`
}

func (m *Manager) createSandbox(req createReq) (*Sandbox, error) {
	b, why := m.bundleValid()
	if b == nil {
		return nil, fmt.Errorf("cannot create a sandbox: %s", why)
	}
	if req.Image == "" {
		req.Image = req.Harness
	}
	if req.Image == "" {
		req.Image = "base"
	}
	ref, ok := b.Images[req.Image]
	if !ok {
		var names []string
		for k := range b.Images {
			names = append(names, k)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("image %q is not in your policy (available: %s); request it with: agentbox allow image:%s --reason \"...\"", req.Image, strings.Join(names, ", "), req.Image)
	}
	s := &Sandbox{ID: newID(), Name: req.Name, Image: req.Image, ImageRef: ref.Ref, Digest: ref.Digest, Harness: ref.Harness,
		Repo: req.Repo, Token: newToken(), SSHPub: req.SSHPub, State: "creating", Desired: "running", Created: time.Now().UTC(),
		Bundle: b.Version, Limits: b.Limits}
	if s.Name == "" {
		s.Name = s.ID
	}
	m.mu.Lock()
	for _, o := range m.sandboxes {
		if o.Name == s.Name {
			m.mu.Unlock()
			return nil, fmt.Errorf("a sandbox named %q already exists", s.Name)
		}
	}
	ip, err := m.allocIP()
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	s.IP = ip
	m.sandboxes[s.ID] = s
	m.tokens[s.Token] = s.ID
	m.byIP[s.IP] = s.ID
	m.mu.Unlock()
	os.MkdirAll(filepath.Join(m.sbDir(s.ID), "workspace"), 0o755)
	m.saveSandbox(s)
	m.emit("sandbox.created", map[string]any{"agentbox.sandbox": s.ID, "name": s.Name, "image": s.Image, "image.digest": s.Digest, "harness": s.Harness, "repo": s.Repo, "agentbox.bundle_version": b.Version})
	if err := m.startSandbox(s.ID); err != nil {
		return s, err
	}
	return s, nil
}

func (m *Manager) allocIP() (string, error) {
	used := map[string]bool{}
	for _, s := range m.sandboxes {
		used[s.IP] = true
	}
	for i := 2; i < 250; i++ {
		ip := fmt.Sprintf("10.88.0.%d", i)
		if !used[ip] {
			return ip, nil
		}
	}
	return "", fmt.Errorf("no free sandbox addresses")
}

func (m *Manager) get(id string) *Sandbox {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s, ok := m.sandboxes[id]; ok {
		return s
	}
	for _, s := range m.sandboxes {
		if s.Name == id {
			return s
		}
	}
	return nil
}

func (m *Manager) setState(s *Sandbox, state, errMsg string) {
	m.mu.Lock()
	s.State, s.Error = state, errMsg
	m.mu.Unlock()
	m.saveSandbox(s)
	go m.heartbeat()
}

func (m *Manager) startSandbox(idOrName string) error {
	s := m.get(idOrName)
	if s == nil {
		return fmt.Errorf("no sandbox %q", idOrName)
	}
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	// Another caller (for example the automatic restore after a reboot) may
	// have started it while we waited for the lock.
	m.mu.RLock()
	state := s.State
	m.mu.RUnlock()
	if state == "running" {
		if out, err := exec.Command("runsc", "--root", "/run/runsc", "state", s.ID).Output(); err == nil && strings.Contains(string(out), `"running"`) {
			return nil
		}
	}
	start := time.Now()
	rootfs, err := m.ensureImage(s.ImageRef, s.Digest)
	if err != nil {
		m.setState(s, "failed", err.Error())
		return fmt.Errorf("image: %w", err)
	}
	dir := m.sbDir(s.ID)
	if err := m.writeSandboxFiles(s, dir); err != nil {
		m.setState(s, "failed", err.Error())
		return err
	}
	if err := m.sandboxNet(s); err != nil {
		m.setState(s, "failed", err.Error())
		return fmt.Errorf("network: %w", err)
	}
	spec := m.ociSpec(s, rootfs, dir)
	sb, _ := json.MarshalIndent(spec, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "config.json"), sb, 0o644); err != nil {
		return err
	}
	exec.Command("runsc", "--root", "/run/runsc", "delete", "--force", s.ID).Run()
	args := append([]string{"--root", "/run/runsc", "--platform=systrap", "--network=sandbox", "--overlay2=root:memory",
		"--pod-init-config=" + filepath.Join(dir, "trace.json")}, m.cfg.RunscFlags...)
	args = append(args, "run", "--detach", "--bundle", dir, s.ID)
	logf, _ := os.OpenFile(filepath.Join(dir, "console.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	cmd := exec.Command("runsc", args...)
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Run(); err != nil {
		logf.Close()
		tail, _ := exec.Command("tail", "-n", "5", filepath.Join(dir, "console.log")).Output()
		m.setState(s, "failed", fmt.Sprintf("runsc: %v: %s", err, tail))
		return fmt.Errorf("runsc run: %v: %s", err, strings.TrimSpace(string(tail)))
	}
	logf.Close()
	m.mu.Lock()
	s.State, s.Error, s.Started, s.StopNote, s.StoppedBy, s.Desired = "running", "", time.Now().UTC(), "", "", "running"
	m.mu.Unlock()
	m.saveSandbox(s)
	log.Printf("sandbox %s (%s) running at %s in %s", s.ID, s.Image, s.IP, time.Since(start).Round(time.Millisecond))
	m.emit("sandbox.started", map[string]any{"agentbox.sandbox": s.ID, "image": s.Image, "image.digest": s.Digest, "ip": s.IP, "start_ms": time.Since(start).Milliseconds()})
	go m.heartbeat()
	return nil
}

func (m *Manager) writeSandboxFiles(s *Sandbox, dir string) error {
	env := m.sandboxEnv(s)
	files := map[string]string{
		"resolv.conf": "nameserver 10.88.0.1\noptions ndots:0\n",
		"hosts":       "127.0.0.1 localhost\n10.88.0.1 llm.agentbox otel.agentbox agentbox\n" + s.IP + " " + s.ID + "\n",
		"agentbox.env": strings.Join(env, "\n") + "\n",
		"authorized_keys": s.SSHPub + "\n",
		"hostname":    s.ID + "\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	trace := map[string]any{"trace_session": map[string]any{
		"name": "Default",
		"points": []any{
			map[string]any{"name": "sentry/execve", "context_fields": []string{"container_id", "cwd", "process_name", "group_id", "parent_thread_group_id", "time"}, "optional_fields": []string{"binary_info"}},
			map[string]any{"name": "syscall/connect/exit", "context_fields": []string{"container_id", "process_name", "group_id", "time"}},
			map[string]any{"name": "container/start", "context_fields": []string{"container_id", "time"}},
		},
		"sinks": []any{map[string]any{"name": "remote", "config": map[string]any{"endpoint": filepath.Join(m.cfg.RunDir, "trace.sock")}, "ignore_setup_error": true}},
	}}
	tb, _ := json.MarshalIndent(trace, "", "  ")
	return os.WriteFile(filepath.Join(dir, "trace.json"), tb, 0o644)
}

// modelEnv names the models a harness should use, from the device's policy:
// the first allowed model is the default and every role a harness asks for by a
// vendor name (Claude Code's opus, sonnet, haiku and sub-agent models) maps to
// an allowed model, so no request is denied for naming a model the policy does
// not list. Harnesses that read their own config files take the model from
// AGENTBOX_MODEL.
func smallModel(models []string) string {
	// The first model whose name says it is a small, fast one (claude-haiku,
	// small, gpt-mini, gemini-flash); otherwise the default model.
	for _, name := range models {
		for _, tok := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') }) {
			switch tok {
			case "small", "haiku", "mini", "fast", "flash", "nano", "lite":
				return name
			}
		}
	}
	return models[0]
}

func (m *Manager) modelEnv(s *Sandbox) []string {
	b := m.currentBundle()
	if b == nil || len(b.Models) == 0 {
		return s.ModelEnv
	}
	def := b.Models[0]
	small := smallModel(b.Models)
	s.ModelEnv = []string{
		"AGENTBOX_MODEL=" + def,
		"AGENTBOX_SMALL_MODEL=" + small,
		"AGENTBOX_MODELS=" + strings.Join(b.Models, ","),
		"ANTHROPIC_MODEL=" + def,
		"ANTHROPIC_DEFAULT_OPUS_MODEL=" + def,
		"ANTHROPIC_DEFAULT_SONNET_MODEL=" + def,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL=" + small,
		"ANTHROPIC_SMALL_FAST_MODEL=" + small,
		"CLAUDE_CODE_SUBAGENT_MODEL=" + def,
	}
	return s.ModelEnv
}

func (m *Manager) sandboxEnv(s *Sandbox) []string {
	return append(m.modelEnv(s), []string{
		"AGENTBOX_SANDBOX=" + s.ID,
		"AGENTBOX_TOKEN=" + s.Token,
		"AGENTBOX_REPO=" + s.Repo,
		"OPENAI_BASE_URL=http://llm.agentbox:4000/v1",
		"OPENAI_API_KEY=" + s.Token,
		"ANTHROPIC_BASE_URL=http://llm.agentbox:4000",
		"ANTHROPIC_API_KEY=" + s.Token,
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://otel.agentbox:4318",
		"OTEL_RESOURCE_ATTRIBUTES=agentbox.sandbox=" + s.ID,
		"HTTP_PROXY=http://10.88.0.1:3128",
		"HTTPS_PROXY=http://10.88.0.1:3128",
		"http_proxy=http://10.88.0.1:3128",
		"https_proxy=http://10.88.0.1:3128",
		"NO_PROXY=llm.agentbox,otel.agentbox,10.88.0.1,localhost,127.0.0.1",
		"no_proxy=llm.agentbox,otel.agentbox,10.88.0.1,localhost,127.0.0.1",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		"TERM=xterm-256color",
		"LANG=C.UTF-8",
	}...)
}

func (m *Manager) ociSpec(s *Sandbox, rootfs, dir string) map[string]any {
	caps := []string{"CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_FSETID", "CAP_FOWNER", "CAP_MKNOD", "CAP_NET_RAW", "CAP_SETGID",
		"CAP_SETUID", "CAP_SETFCAP", "CAP_SETPCAP", "CAP_NET_BIND_SERVICE", "CAP_SYS_CHROOT", "CAP_KILL", "CAP_AUDIT_WRITE"}
	bind := func(dst, src string, ro bool) map[string]any {
		opts := []string{"rbind", "rw"}
		if ro {
			opts = []string{"rbind", "ro"}
		}
		return map[string]any{"destination": dst, "type": "bind", "source": src, "options": opts}
	}
	mem := int64(s.Limits.MemoryMB) * 1024 * 1024
	if mem <= 0 {
		mem = 2 << 30
	}
	pids := int64(s.Limits.PIDs)
	if pids <= 0 {
		pids = 512
	}
	return map[string]any{
		"ociVersion": "1.0.0",
		"process": map[string]any{
			"user": map[string]any{"uid": 0, "gid": 0},
			"args": []string{"/usr/local/bin/sandbox-init"},
			"env":  m.sandboxEnv(s),
			"cwd":  "/",
			"capabilities": map[string]any{"bounding": caps, "effective": caps, "permitted": caps},
			"rlimits":      []any{map[string]any{"type": "RLIMIT_NOFILE", "hard": 65536, "soft": 65536}},
			"noNewPrivileges": false,
		},
		// Writable inside the sandbox, but runsc runs with --overlay2=root:memory,
		// so writes land in a per-sandbox overlay and the cached image on the
		// data disk is never modified.
		"root":     map[string]any{"path": rootfs, "readonly": false},
		"hostname": s.ID,
		"mounts": []any{
			map[string]any{"destination": "/proc", "type": "proc", "source": "proc"},
			map[string]any{"destination": "/dev", "type": "tmpfs", "source": "tmpfs", "options": []string{"nosuid", "strictatime", "mode=755", "size=65536k"}},
			map[string]any{"destination": "/dev/pts", "type": "devpts", "source": "devpts", "options": []string{"nosuid", "noexec", "newinstance", "ptmxmode=0666", "mode=0620"}},
			map[string]any{"destination": "/dev/shm", "type": "tmpfs", "source": "shm", "options": []string{"nosuid", "noexec", "nodev", "mode=1777", "size=65536k"}},
			map[string]any{"destination": "/sys", "type": "sysfs", "source": "sysfs", "options": []string{"nosuid", "noexec", "nodev", "ro"}},
			map[string]any{"destination": "/tmp", "type": "tmpfs", "source": "tmpfs", "options": []string{"nosuid", "nodev", "mode=1777"}},
			bind("/workspace", filepath.Join(dir, "workspace"), false),
			bind("/etc/resolv.conf", filepath.Join(dir, "resolv.conf"), true),
			bind("/etc/hosts", filepath.Join(dir, "hosts"), true),
			bind("/etc/hostname", filepath.Join(dir, "hostname"), true),
			bind("/etc/agentbox/env", filepath.Join(dir, "agentbox.env"), true),
			bind("/etc/agentbox/authorized_keys", filepath.Join(dir, "authorized_keys"), true),
		},
		"linux": map[string]any{
			"namespaces": []any{
				map[string]any{"type": "pid"}, map[string]any{"type": "ipc"}, map[string]any{"type": "uts"}, map[string]any{"type": "mount"},
				map[string]any{"type": "network", "path": "/var/run/netns/" + nsName(s.ID)},
			},
			"resources": map[string]any{
				"memory": map[string]any{"limit": mem},
				"pids":   map[string]any{"limit": pids},
			},
		},
	}
}

// stopSandbox kills the sandbox and keeps its workspace.
func (m *Manager) stopSandbox(idOrName, reason, by string) error {
	s := m.get(idOrName)
	if s == nil {
		return fmt.Errorf("no sandbox %q", idOrName)
	}
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	start := time.Now()
	exec.Command("runsc", "--root", "/run/runsc", "kill", s.ID, "KILL").Run()
	exec.Command("runsc", "--root", "/run/runsc", "delete", "--force", s.ID).Run()
	// End shells, agent tasks and previews attached to this sandbox at once.
	closeTunnels(s.ID)
	m.mu.Lock()
	s.State, s.StopNote, s.StoppedBy, s.Desired = "stopped", reason, by, "stopped"
	m.mu.Unlock()
	m.saveSandbox(s)
	log.Printf("sandbox %s stopped in %s: %s", s.ID, time.Since(start).Round(time.Millisecond), reason)
	m.emit("sandbox.stopped", map[string]any{"agentbox.sandbox": s.ID, "reason": reason, "stopped_by": by})
	go m.heartbeat()
	return nil
}

func (m *Manager) removeSandbox(idOrName string) error {
	s := m.get(idOrName)
	if s == nil {
		return fmt.Errorf("no sandbox %q", idOrName)
	}
	m.stopSandbox(s.ID, "removed by user", s.ID)
	m.teardownNet(s)
	os.RemoveAll(m.sbDir(s.ID))
	m.mu.Lock()
	delete(m.sandboxes, s.ID)
	delete(m.tokens, s.Token)
	delete(m.byIP, s.IP)
	m.mu.Unlock()
	m.emit("sandbox.removed", map[string]any{"agentbox.sandbox": s.ID})
	go m.heartbeat()
	return nil
}

func (m *Manager) stopAll(reason string, keepDesired bool) {
	m.mu.RLock()
	var ids []string
	for id, s := range m.sandboxes {
		if s.State == "running" {
			ids = append(ids, id)
		}
	}
	m.mu.RUnlock()
	for _, id := range ids {
		s := m.get(id)
		exec.Command("runsc", "--root", "/run/runsc", "kill", id, "KILL").Run()
		exec.Command("runsc", "--root", "/run/runsc", "delete", "--force", id).Run()
		m.mu.Lock()
		s.State = "stopped"
		if !keepDesired {
			s.Desired = "stopped"
		}
		s.StopNote = reason
		m.mu.Unlock()
		m.saveSandbox(s)
	}
}

// restoreSandboxes reloads sandbox records after an appliance (re)boot and
// restarts the ones that were running.
func (m *Manager) restoreSandboxes() {
	dirs, _ := filepath.Glob(filepath.Join(m.cfg.DataDir, "sandboxes", "*", "meta.json"))
	for _, f := range dirs {
		var d sandboxDisk
		if loadJSON(f, &d) != nil {
			continue
		}
		s := d.Sandbox
		s.Token, s.SSHPub = d.Token, d.SSHPub
		if s.State == "running" || s.State == "creating" {
			s.State = "stopped"
		}
		m.mu.Lock()
		m.sandboxes[s.ID] = &s
		m.tokens[s.Token] = s.ID
		m.byIP[s.IP] = s.ID
		m.mu.Unlock()
	}
	go func() {
		m.mu.RLock()
		var ids []string
		for id, s := range m.sandboxes {
			if s.Desired == "running" {
				ids = append(ids, id)
			}
		}
		m.mu.RUnlock()
		for _, id := range ids {
			if err := m.startSandbox(id); err != nil {
				log.Printf("restore %s: %v", id, err)
			}
		}
	}()
}

func (m *Manager) sandboxList() []Sandbox {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Sandbox, 0, len(m.sandboxes))
	for _, s := range m.sandboxes {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}
