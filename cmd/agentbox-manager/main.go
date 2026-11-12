// agentbox-manager is the only privileged process in the appliance that users
// reach (through the host service). It attests the appliance, turns the signed
// policy bundle into gVisor sandboxes, networks and tunnels, and refuses
// anything the policy does not allow. It also supervises the local Envoy and
// OpenTelemetry Collector.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"agentbox/pkg/bundle"
	"agentbox/pkg/otlp"
	"agentbox/pkg/tpm"
	"agentbox/pkg/vsockrpc"
)

type Config struct {
	ControlListen string `json:"control_listen"`
	TPMSocket     string `json:"tpm_socket"`
	DataDir       string `json:"data_dir"`
	RunDir        string `json:"run_dir"`
	PolicyPubKey  string `json:"policy_pubkey"`
	EnrollURL     string `json:"enroll_url"`
	PermsvcURL    string `json:"permsvc_url"`
	Registry      string `json:"registry"`
	GatewayHost   string `json:"gateway_host"`
	GatewayPort   int    `json:"gateway_port"`
	TelemetryURL  string `json:"telemetry_url"`
	BridgeCIDR    string `json:"bridge_cidr"`
	RunscFlags    []string `json:"runsc_flags"`
	EnvoyBin      string `json:"envoy_bin"`
	OtelcolBin    string `json:"otelcol_bin"`
}

type Release struct {
	Version string `json:"version"`
	Flavor  string `json:"flavor"`
}

// Persisted appliance state on the data disk.
type State struct {
	DeviceID string          `json:"device_id"`
	User     string          `json:"user"`
	CARoot   string          `json:"ca_root"`
	Bundle   *bundle.Signed  `json:"bundle,omitempty"`
}

type AttestStatus struct {
	Status      string    `json:"status"` // pending | pass | fail | unconfigured
	Reason      string    `json:"reason,omitempty"`
	Release     string    `json:"release,omitempty"`
	CertExpires time.Time `json:"cert_expires,omitempty"`
	LastAttempt time.Time `json:"last_attempt,omitempty"`
}

type Manager struct {
	cfg     Config
	rel     Release
	pub     ed25519.PublicKey
	ev      *otlp.Exporter
	tpm     tpm.Conn
	ak      *tpm.AK
	akPub   []byte
	ekPub   []byte

	mu        sync.RWMutex
	st        State
	bundle    *bundle.Bundle // verified, currently applied
	attest    AttestStatus
	cpReach   string // last control-plane error, "" when fine
	sandboxes map[string]*Sandbox
	tokens    map[string]string // token -> sandbox id
	byIP      map[string]string // ip -> sandbox id
	rate      map[string]*bucket

	certMu   sync.RWMutex
	certPath string
	keyPath  string
	caPath   string

	wake chan struct{} // nudges the sync loop
	quit chan struct{}
}

func loadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	os.MkdirAll(filepath.Dir(path), 0o755)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func main() {
	cfgPath := "/etc/agentbox/appliance.json"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	var cfg Config
	if err := loadJSON(cfgPath, &cfg); err != nil {
		log.Fatalf("config: %v", err)
	}
	// The measured config file stays fixed; the deployment picks the private
	// host-channel address (the per-PC network that stands in for vsock).
	if v := os.Getenv("AGENTBOX_CONTROL_LISTEN"); v != "" {
		cfg.ControlListen = v
	}
	m := &Manager{cfg: cfg, sandboxes: map[string]*Sandbox{}, tokens: map[string]string{}, byIP: map[string]string{},
		rate: map[string]*bucket{}, wake: make(chan struct{}, 1), quit: make(chan struct{})}
	loadJSON("/etc/agentbox/release.json", &m.rel)
	var err error
	if m.pub, err = bundle.LoadPublicKey(cfg.PolicyPubKey); err != nil {
		log.Fatalf("policy root key: %v", err)
	}
	certs := filepath.Join(cfg.RunDir, "certs")
	m.certPath, m.keyPath, m.caPath = filepath.Join(certs, "appliance.crt"), filepath.Join(certs, "appliance.key"), filepath.Join(certs, "ca.crt")
	loadJSON(filepath.Join(cfg.DataDir, "state.json"), &m.st)
	m.attest = AttestStatus{Status: "unconfigured"}
	if m.st.DeviceID != "" {
		m.attest.Status = "pending"
	}
	res := map[string]string{"service.name": "agentbox-manager", "agentbox.image": m.rel.Version, "agentbox.flavor": m.rel.Flavor}
	m.ev = otlp.New("http://127.0.0.1:4318/v1/logs", res, nil)
	if m.st.DeviceID != "" {
		m.ev.SetResource("agentbox.device", m.st.DeviceID)
		m.ev.SetResource("agentbox.user", m.st.User)
	}

	// The control channel comes up first and stays up whatever else fails: it is how
	// `agentbox doctor` learns why something did not work. Nothing below this point
	// may end the process.
	go keepTrying("control channel", func() error {
		lis, err := vsockrpc.Listen(cfg.ControlListen)
		if err != nil {
			return err
		}
		recordFault("control channel", nil)
		log.Printf("agentbox-manager %s (%s): control channel %s", m.rel.Version, m.rel.Flavor, cfg.ControlListen)
		return (&http.Server{Handler: m.api(), ReadHeaderTimeout: 10 * time.Second}).Serve(lis)
	})
	// Identity keys in the (virtual) TPM; retried because the device can appear late.
	go retryUntil("tpm", m.initTPM)

	// A previously applied bundle keeps sandboxes working offline (O3).
	if m.st.Bundle != nil {
		if b, err := bundle.Verify(m.st.Bundle, m.pub, m.st.DeviceID, 0, time.Unix(0, 0)); err == nil {
			m.bundle = b
			log.Printf("restored bundle v%d (expires %s)", b.Version, b.ExpiresAt.Format(time.RFC3339))
		}
	}

	// Try once now (Envoy needs the bridge and the placeholder certificate to start),
	// and keep retrying in the background if it fails.
	if err := m.setupNetwork(); err != nil {
		log.Printf("network: %v", err)
		recordFault("network", err)
		go retryUntil("network", m.setupNetwork)
	}
	if err := m.placeholderCert(); err != nil {
		log.Printf("placeholder certificate: %v", err)
		recordFault("placeholder certificate", err)
		go retryUntil("placeholder certificate", m.placeholderCert)
	}
	m.supervise("envoy", cfg.EnvoyBin, "-c", "/etc/agentbox/envoy.yaml", "--log-level", "warn", "--base-id", "1")
	m.supervise("otelcol", cfg.OtelcolBin, "--config", "/etc/agentbox/otelcol.yaml")

	go keepTrying("model authorization", m.serveAuthz)
	go keepTrying("egress proxy", m.serveEgress)
	go keepTrying("dns", m.serveDNS)
	go keepTrying("otlp proxy", m.serveOTLPProxy)
	go m.gatewayProbe()
	go keepTrying("gvisor trace sink", m.serveTrace)
	go m.attestLoop()
	go m.syncLoop()
	go m.heartbeatLoop()
	m.restoreSandboxes()

	log.Printf("agentbox-manager %s (%s) started", m.rel.Version, m.rel.Flavor)
	m.ev.Emit("appliance.boot", map[string]any{"release": m.rel.Version, "flavor": m.rel.Flavor})

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	select {
	case <-sig:
	case <-m.quit:
	}
	log.Printf("shutting down: stopping sandboxes")
	m.stopAll("appliance shutdown", true) // a reboot: sandboxes come back on the next boot
	time.Sleep(1500 * time.Millisecond) // let the exporter flush
}

func (m *Manager) saveState() {
	m.mu.RLock()
	b, _ := json.MarshalIndent(m.st, "", "  ")
	m.mu.RUnlock()
	if err := writeFileAtomic(filepath.Join(m.cfg.DataDir, "state.json"), b, 0o600); err != nil {
		log.Printf("save state: %v", err)
	}
}

func (m *Manager) nudge() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) currentBundle() *bundle.Bundle {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.bundle
}

// bundleValid reports whether the policy still permits the model endpoint and
// egress. An expired bundle keeps sandboxes running but closes both.
func (m *Manager) bundleValid() (*bundle.Bundle, string) {
	b := m.currentBundle()
	if b == nil {
		return nil, "no policy bundle yet (appliance not enrolled or never reached the control plane)"
	}
	if time.Now().After(b.ExpiresAt) {
		return b, "policy bundle expired and could not be renewed (control plane unreachable)"
	}
	return b, ""
}

// NonModelGrace is how long permissions other than model access (egress,
// images, ports) keep applying after the policy bundle has expired and could not
// be renewed: the control plane may be down for a weekend. Model access ends at
// expiry (bundleValid).
const NonModelGrace = 72 * time.Hour

// bundleUsable is bundleValid for everything that is not a model call.
func (m *Manager) bundleUsable() (*bundle.Bundle, string) {
	b := m.currentBundle()
	if b == nil {
		return nil, "no policy bundle yet (appliance not enrolled or never reached the control plane)"
	}
	if time.Now().After(b.ExpiresAt.Add(NonModelGrace)) {
		return b, "policy bundle expired more than 72 hours ago and could not be renewed (control plane unreachable)"
	}
	return b, ""
}

func (m *Manager) emit(name string, attrs map[string]any) { m.ev.Emit(name, attrs) }

var _ = context.Background

// initTPM opens the TPM and creates the attestation key. Until it succeeds the
// appliance cannot attest, but everything else (and the control channel) works.
func (m *Manager) initTPM() error {
	conn, err := tpm.Open(m.cfg.TPMSocket)
	if err != nil {
		return err
	}
	ak, err := tpm.LoadAK(conn)
	if err != nil {
		conn.Close()
		return fmt.Errorf("create attestation key: %w", err)
	}
	// The endorsement key is informational (the enrollment binds the attestation key);
	// a TPM that will not create one must not stop the appliance.
	ek, err := tpm.EKPublic(conn)
	if err != nil {
		log.Printf("endorsement key unavailable (continuing): %v", err)
		recordFault("tpm endorsement key", err)
		ek = nil
	}
	m.mu.Lock()
	m.tpm, m.ak, m.akPub, m.ekPub = conn, ak, ak.Public, ek
	m.mu.Unlock()
	log.Printf("TPM ready (%s)", m.cfg.TPMSocket)
	kickAttest()
	return nil
}

// tpmState returns the TPM objects once initTPM has succeeded.
func (m *Manager) tpmState() (conn tpm.Conn, ak *tpm.AK, akPub, ekPub []byte, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tpm, m.ak, m.akPub, m.ekPub, m.tpm != nil && m.ak != nil
}
