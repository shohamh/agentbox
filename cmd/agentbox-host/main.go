// agentbox-host owns the appliance VM on a PC and is the only thing the CLI
// talks to. It enrolls the device (OIDC sign-in, device certificate from
// step-ca, appliance identity), forwards sandbox and permission calls to
// agentbox-manager over the host-to-appliance channel (vsock in production),
// and carries SSH and preview tunnels.
package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agentbox/pkg/hostapi"
	"agentbox/pkg/identity"
	"agentbox/pkg/pki"
	"agentbox/pkg/vsockrpc"
)

type Config struct {
	Driver        string `json:"driver"`         // container | qemu | hyperv
	ApplianceAddr string `json:"appliance_addr"` // container driver: tcp://appliance:7000
	VsockCID      int    `json:"vsock_cid"`
	StateDir      string `json:"state_dir"`
	BaseImage     string `json:"base_image"`
	Socket        string `json:"socket"`
	EnrollURL     string `json:"enroll_url"`
	OIDCIssuer    string `json:"oidc_issuer"` // as the PC reaches it
	OIDCClientID  string `json:"oidc_client_id"`
	CARootFile    string `json:"ca_root_file"`
	DeviceID      string `json:"device_id"`
	// Test beds only: allow username/password sign-in in place of the device
	// code flow so suites can run unattended.
	AllowPasswordGrant bool `json:"allow_password_grant"`
	// Name resolution overrides for PCs that cannot resolve the corporate
	// names (test beds): "host:port" -> "address:port". The URL and TLS server
	// name stay as configured; only the TCP destination changes.
	HostMap map[string]string `json:"host_map"`
	// Hyper-V driver settings.
	SwitchName string `json:"switch_name"` // default "Default Switch"
	MemoryMB   int    `json:"memory_mb"`   // default 4096
	CPUs       int    `json:"cpus"`        // default 4
	DataGB     int    `json:"data_gb"`     // default 20
	LogFile    string `json:"log_file"`
}

type host struct {
	cfg    Config
	drv    Driver
	app    *http.Client
	caPool *x509.CertPool
}

// onStop runs when the Windows service is asked to stop.
var onStop func()

func main() {
	path := "/etc/agentbox/host.json"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	// As a Windows service the process must answer the service manager.
	if runAsService(func() { serve(path) }) {
		return
	}
	serve(path)
}

func serve(path string) {
	var cfg Config
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	// Windows editors and PowerShell 5.1 write a UTF-8 byte-order mark.
	if err := json.Unmarshal(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), &cfg); err != nil {
		log.Fatal(err)
	}
	if cfg.LogFile != "" {
		if f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			log.SetOutput(f)
		}
	}
	if cfg.DeviceID == "" {
		cfg.DeviceID, _ = os.Hostname()
	}
	if cfg.Socket == "" {
		cfg.Socket = hostapi.DefaultAddress()
	}
	if cfg.OIDCClientID == "" {
		cfg.OIDCClientID = "agentbox-cli"
	}
	os.MkdirAll(cfg.StateDir, 0o700)
	drv, err := newDriver(cfg)
	if err != nil {
		log.Fatal(err)
	}
	h := &host{cfg: cfg, drv: drv}
	h.app = vsockrpc.Client(drv.ChannelAddr(), 90*time.Second)
	if pem, err := os.ReadFile(cfg.CARootFile); err == nil {
		h.caPool = x509.NewCertPool()
		h.caPool.AppendCertsFromPEM(pem)
	}
	if cfg.Driver != "container" {
		// Start in the background: the CLI works (and reports) while the VM boots.
		go func() {
			if err := drv.Start(context.Background()); err != nil {
				log.Printf("start appliance: %v", err)
			}
		}()
		onStop = func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := drv.Stop(ctx); err != nil {
				log.Printf("stop appliance: %v", err)
			}
		}
	}
	lis, err := hostapi.Listen(cfg.Socket)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("agentbox-host (%s driver) on %s, device %s", drv.Name(), cfg.Socket, cfg.DeviceID)
	log.Fatal((&http.Server{Handler: h.routes()}).Serve(lis))
}

func out(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func bad(w http.ResponseWriter, code int, err error) { out(w, code, map[string]string{"error": err.Error()}) }

func (h *host) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", h.status)
	mux.HandleFunc("POST /v1/enroll", h.enroll)
	mux.HandleFunc("POST /v1/enroll/device-start", h.deviceStart)
	mux.HandleFunc("POST /v1/enroll/device-finish", h.deviceFinish)
	mux.HandleFunc("GET /v1/checks", func(w http.ResponseWriter, _ *http.Request) { out(w, 200, h.drv.Checks()) })
	mux.HandleFunc("GET /v1/sandboxes/{id}/tunnel", h.tunnel)
	mux.HandleFunc("GET /v1/sandboxes/{id}/preview", h.tunnel)
	mux.HandleFunc("/v1/", h.forward) // sandboxes, permissions, recycle
	return mux
}

// forward relays an API call to agentbox-manager.
func (h *host) forward(w http.ResponseWriter, r *http.Request) {
	req, _ := http.NewRequest(r.Method, "http://appliance"+r.URL.RequestURI(), r.Body)
	req.Header = r.Header.Clone()
	resp, err := h.app.Do(req)
	if err != nil {
		bad(w, 503, fmt.Errorf("the appliance is not reachable (%v): run agentbox status", err))
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// tunnel joins the CLI's stream to the appliance's tunnel or preview endpoint.
func (h *host) tunnel(w http.ResponseWriter, r *http.Request) {
	up, err := vsockrpc.Dial(r.Context(), h.drv.ChannelAddr())
	if err != nil {
		bad(w, 503, fmt.Errorf("the appliance is not reachable: %v", err))
		return
	}
	conn, err := hostapi.Upgrade(up, r.URL.RequestURI())
	if err != nil {
		bad(w, 502, err)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		conn.Close()
		return
	}
	c, buf, err := hj.Hijack()
	if err != nil {
		conn.Close()
		return
	}
	c.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: agentbox-tunnel\r\nConnection: Upgrade\r\n\r\n"))
	done := make(chan struct{}, 2)
	go func() {
		if n := buf.Reader.Buffered(); n > 0 {
			b, _ := buf.Reader.Peek(n)
			conn.Write(b)
		}
		io.Copy(conn, c)
		done <- struct{}{}
	}()
	go func() { io.Copy(c, conn); done <- struct{}{} }()
	<-done
	c.Close()
	conn.Close()
}

func (h *host) applianceGet(path string, v any) error {
	resp, err := h.app.Get("http://appliance" + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, bytes.TrimSpace(b))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func (h *host) status(w http.ResponseWriter, _ *http.Request) {
	res := map[string]any{"driver": h.drv.Name(), "device_id": h.cfg.DeviceID, "checks": h.drv.Checks()}
	var app map[string]any
	if err := h.applianceGet("/v1/status", &app); err != nil {
		res["appliance"] = map[string]any{"reachable": false, "error": err.Error(),
			"fix": "run agentbox up (or restart the agentbox-host service); on Windows check that Hyper-V is enabled"}
	} else {
		app["reachable"] = true
		res["appliance"] = app
	}
	_, err := os.Stat(filepath.Join(h.cfg.StateDir, "device.crt"))
	res["enrolled"] = err == nil
	out(w, 200, res)
}

// ---------- enrollment ----------

// transport dials through cfg.HostMap.
func (h *host) transport(tc *tls.Config) *http.Transport {
	return &http.Transport{
		TLSClientConfig: tc,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if to, ok := h.cfg.HostMap[addr]; ok {
				addr = to
			}
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
}

func (h *host) oidcClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second, Transport: h.transport(nil)}
}

func (h *host) tokenEndpoint() string { return strings.TrimRight(h.cfg.OIDCIssuer, "/") + "/protocol/openid-connect/token" }

func (h *host) form(u string, vals url.Values, v any) error {
	resp, err := h.oidcClient().PostForm(u, vals)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %s", resp.Status, bytes.TrimSpace(data))
	}
	return nil
}

type tokenResp struct {
	AccessToken string `json:"access_token"`
	Error       string `json:"error"`
	Desc        string `json:"error_description"`
}

func (h *host) deviceStart(w http.ResponseWriter, _ *http.Request) {
	var d struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		URI        string `json:"verification_uri"`
		URIComplete string `json:"verification_uri_complete"`
		Interval   int    `json:"interval"`
		Expires    int    `json:"expires_in"`
	}
	err := h.form(strings.TrimRight(h.cfg.OIDCIssuer, "/")+"/protocol/openid-connect/auth/device", url.Values{"client_id": {h.cfg.OIDCClientID}}, &d)
	if err != nil || d.DeviceCode == "" {
		bad(w, 502, fmt.Errorf("sign-in service unavailable: %v", err))
		return
	}
	out(w, 200, d)
}

func (h *host) deviceFinish(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceCode string `json:"device_code"`
		Interval   int    `json:"interval"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	if in.Interval < 1 {
		in.Interval = 3
	}
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		var t tokenResp
		err := h.form(h.tokenEndpoint(), url.Values{"client_id": {h.cfg.OIDCClientID}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {in.DeviceCode}}, &t)
		if err == nil && t.AccessToken != "" {
			h.finishEnroll(w, r, t.AccessToken)
			return
		}
		if t.Error != "" && t.Error != "authorization_pending" && t.Error != "slow_down" {
			bad(w, 401, fmt.Errorf("sign-in failed: %s %s", t.Error, t.Desc))
			return
		}
		time.Sleep(time.Duration(in.Interval) * time.Second)
	}
	bad(w, 408, fmt.Errorf("sign-in timed out"))
}

func (h *host) enroll(w http.ResponseWriter, r *http.Request) {
	var in struct{ Username, Password string }
	json.NewDecoder(r.Body).Decode(&in)
	if !h.cfg.AllowPasswordGrant {
		bad(w, 403, fmt.Errorf("password sign-in is disabled; use the device-code flow (agentbox enroll)"))
		return
	}
	var t tokenResp
	err := h.form(h.tokenEndpoint(), url.Values{"client_id": {h.cfg.OIDCClientID}, "grant_type": {"password"}, "username": {in.Username}, "password": {in.Password}}, &t)
	if err != nil || t.AccessToken == "" {
		bad(w, 401, fmt.Errorf("sign-in failed: %s %s %v", t.Error, t.Desc, err))
		return
	}
	h.finishEnroll(w, r, t.AccessToken)
}

// finishEnroll registers the device with the control plane and configures the
// appliance with its identity.
func (h *host) finishEnroll(w http.ResponseWriter, r *http.Request, token string) {
	var id struct {
		AK []byte `json:"ak_public"`
		EK []byte `json:"ek_public"`
	}
	if err := h.applianceGet("/v1/identity", &id); err != nil {
		bad(w, 503, fmt.Errorf("the appliance is not reachable: %v", err))
		return
	}
	devKeyPath := filepath.Join(h.cfg.StateDir, "device.key")
	var key *ecdsa.PrivateKey
	if b, err := os.ReadFile(devKeyPath); err == nil {
		if k, err := parseKey(b); err == nil {
			key = k
		}
	}
	if key == nil {
		key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
	keyPEM, csr, err := pki.CSRForKey(key, h.cfg.DeviceID, []string{"device." + h.cfg.DeviceID})
	if err != nil {
		bad(w, 500, err)
		return
	}
	var sshPub string
	if b, err := os.ReadFile(filepath.Join(h.cfg.StateDir, "ssh.pub")); err == nil {
		sshPub = strings.TrimSpace(string(b))
	}
	hn, _ := os.Hostname()
	req := map[string]any{"access_token": token, "device_id": h.cfg.DeviceID, "hostname": hn, "os": osName(), "csr": string(csr),
		"ak_public": id.AK, "ek_public": id.EK, "ssh_public": sshPub}
	var resp struct {
		User       string `json:"user"`
		DeviceCert string `json:"device_cert"`
		CARoot     string `json:"ca_root"`
		Error      string `json:"error"`
	}
	c := &http.Client{Timeout: 30 * time.Second, Transport: h.transport(&tls.Config{RootCAs: h.caPool})}
	b, _ := json.Marshal(req)
	hr, err := c.Post(h.cfg.EnrollURL+"/api/v1/enroll", "application/json", bytes.NewReader(b))
	if err != nil {
		bad(w, 503, fmt.Errorf("control plane unreachable: %v", err))
		return
	}
	defer hr.Body.Close()
	data, _ := io.ReadAll(hr.Body)
	json.Unmarshal(data, &resp)
	if hr.StatusCode >= 300 {
		bad(w, hr.StatusCode, fmt.Errorf("%s", resp.Error))
		return
	}
	os.WriteFile(devKeyPath, keyPEM, 0o600)
	os.WriteFile(filepath.Join(h.cfg.StateDir, "device.crt"), []byte(resp.DeviceCert), 0o644)
	os.WriteFile(filepath.Join(h.cfg.StateDir, "ca.crt"), []byte(resp.CARoot), 0o644)
	cfg, _ := json.Marshal(map[string]string{"device_id": h.cfg.DeviceID, "user": resp.User, "ca_root": resp.CARoot})
	ar, err := h.app.Post("http://appliance/v1/configure", "application/json", bytes.NewReader(cfg))
	if err != nil {
		bad(w, 503, err)
		return
	}
	ar.Body.Close()
	out(w, 200, map[string]any{"user": resp.User, "device_id": h.cfg.DeviceID, "identity": identity.DeviceID(h.cfg.DeviceID, resp.User)})
}

