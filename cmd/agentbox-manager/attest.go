package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"agentbox/pkg/identity"
	"agentbox/pkg/measure"
	"agentbox/pkg/pki"
)

// placeholderCert lets Envoy start before the first attestation. It is
// self-signed, so the front gateway refuses it.
func (m *Manager) placeholderCert() error {
	if _, err := os.Stat(m.certPath); err == nil {
		return nil
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "unattested-appliance"},
		NotBefore: time.Now(), NotAfter: time.Now().Add(24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	kder, _ := x509.MarshalPKCS8PrivateKey(key)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	writeFileAtomic(m.keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder}), 0o600)
	writeFileAtomic(m.caPath, certPEM, 0o644)
	return writeFileAtomic(m.certPath, certPEM, 0o644)
}

// clientTLS presents the current appliance certificate (re-read on each
// handshake so rotations apply at once).
func (m *Manager) clientTLS() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			m.certMu.RLock()
			defer m.certMu.RUnlock()
			c, err := tls.LoadX509KeyPair(m.certPath, m.keyPath)
			if err != nil {
				return &tls.Certificate{}, nil
			}
			return &c, nil
		},
		RootCAs: m.rootPool(),
	}
}

func (m *Manager) rootPool() *x509.CertPool {
	p := x509.NewCertPool()
	m.mu.RLock()
	p.AppendCertsFromPEM([]byte(m.st.CARoot))
	m.mu.RUnlock()
	return p
}

func (m *Manager) mtlsClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &http.Transport{TLSClientConfig: m.clientTLS(), IdleConnTimeout: 30 * time.Second}}
}

func (m *Manager) enrollClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: m.rootPool()}}}
}

func (m *Manager) postJSON(c *http.Client, url string, in, out any) error {
	b, _ := json.Marshal(in)
	resp, err := c.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("%s: %s", resp.Status, bytes.TrimSpace(data))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (m *Manager) attestLoop() {
	for {
		m.mu.RLock()
		configured := m.st.DeviceID != ""
		m.mu.RUnlock()
		wait := 15 * time.Second
		if configured {
			ttl, err := m.attestOnce()
			if err != nil {
				log.Printf("attestation: %v", err)
				m.mu.Lock()
				m.attest.Status, m.attest.Reason, m.attest.LastAttempt = "fail", err.Error(), time.Now()
				m.mu.Unlock()
			} else {
				// Re-attest at a third of the certificate lifetime.
				wait = ttl / 3
			}
		}
		select {
		case <-time.After(wait):
		case <-m.attestNow():
		}
	}
}

var attestKick = make(chan struct{}, 1)

func (m *Manager) attestNow() <-chan struct{} { return attestKick }

func kickAttest() {
	select {
	case attestKick <- struct{}{}:
	default:
	}
}

func (m *Manager) attestOnce() (time.Duration, error) {
	m.mu.RLock()
	dev, user := m.st.DeviceID, m.st.User
	m.mu.RUnlock()
	c := m.enrollClient()
	var n struct {
		Nonce string `json:"nonce"`
	}
	if err := m.postJSON(c, m.cfg.EnrollURL+"/api/v1/attest/nonce", map[string]string{"device_id": dev}, &n); err != nil {
		m.setReach(err)
		return 0, fmt.Errorf("control plane unreachable: %w", err)
	}
	m.setReach(nil)
	nonce, _ := hex.DecodeString(n.Nonce)
	_, ak, _, _, ok := m.tpmState()
	if !ok {
		return 0, fmt.Errorf("the virtual TPM is not ready: %s", faultList()["tpm"])
	}
	q, err := ak.Quote(nonce)
	if err != nil {
		return 0, err
	}
	var evlog []measure.Entry
	if err := loadJSON(filepath.Join(m.cfg.RunDir, "eventlog.json"), &evlog); err != nil {
		return 0, fmt.Errorf("measured boot log missing: %w", err)
	}
	keyPEM, csrPEM, err := pki.NewKeyAndCSR(dev+"-appliance", []string{identity.ApplianceID(dev, user)})
	if err != nil {
		return 0, err
	}
	var out struct {
		Certificate string `json:"certificate"`
		CARoot      string `json:"ca_root"`
		Release     string `json:"release"`
		TTL         int    `json:"ttl_seconds"`
	}
	err = m.postJSON(c, m.cfg.EnrollURL+"/api/v1/attest", map[string]any{
		"device_id": dev, "nonce": n.Nonce, "quote": q, "event_log": evlog, "csr": string(csrPEM), "version": m.rel.Version,
	}, &out)
	if err != nil {
		m.emit("attestation.result", map[string]any{"result": "fail", "reason": err.Error()})
		return 0, err
	}
	m.certMu.Lock()
	writeFileAtomic(m.keyPath, keyPEM, 0o600)
	writeFileAtomic(m.caPath, []byte(out.CARoot), 0o644)
	err = writeFileAtomic(m.certPath, []byte(out.Certificate), 0o644)
	m.certMu.Unlock()
	if err != nil {
		return 0, err
	}
	exp, _ := pki.LeafNotAfter([]byte(out.Certificate))
	m.mu.Lock()
	first := m.attest.Status != "pass"
	m.attest = AttestStatus{Status: "pass", Release: out.Release, CertExpires: exp, LastAttempt: time.Now()}
	m.mu.Unlock()
	if first {
		log.Printf("attestation passed (release %s); appliance certificate valid until %s", out.Release, exp.Format(time.RFC3339))
		m.emit("attestation.result", map[string]any{"result": "pass", "release": out.Release})
		m.nudge()
	}
	ttl := time.Duration(out.TTL) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	return ttl, nil
}

func (m *Manager) setReach(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil {
		m.cpReach = ""
	} else {
		m.cpReach = err.Error()
	}
}

var (
	procMu sync.Mutex
	procs  = map[string]*exec.Cmd{}
)

// restartChild kills a supervised child so it restarts with fresh settings.
func restartChild(name string) {
	procMu.Lock()
	defer procMu.Unlock()
	if c := procs[name]; c != nil && c.Process != nil {
		c.Process.Kill()
	}
}

// supervise runs a child process and restarts it if it exits.
func (m *Manager) supervise(name string, bin string, args ...string) {
	go func() {
		for {
			cmd := exec.Command(bin, args...)
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			m.mu.RLock()
			dev, usr := m.st.DeviceID, m.st.User
			m.mu.RUnlock()
			if dev == "" {
				dev, usr = "unenrolled", "unenrolled" // collector settings reject empty values
			}
			cmd.Env = append(os.Environ(), "AGENTBOX_DEVICE="+dev, "AGENTBOX_USER="+usr, "AGENTBOX_IMAGE="+m.rel.Version)
			start := time.Now()
			err := cmd.Start()
			if err == nil {
				procMu.Lock()
				procs[name] = cmd
				procMu.Unlock()
				err = cmd.Wait()
			}
			log.Printf("%s exited: %v", name, err)
			if time.Since(start) < 5*time.Second {
				time.Sleep(3 * time.Second)
			}
		}
	}()
}
