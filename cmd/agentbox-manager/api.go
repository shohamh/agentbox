package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func jsonOut(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func errOut(w http.ResponseWriter, code int, err error) {
	jsonOut(w, code, map[string]string{"error": err.Error()})
}

// api is served only on the host channel (vsock), never on the sandbox
// network, so code in a sandbox cannot reach it (S5).
func (m *Manager) api() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", m.apiStatus)
	mux.HandleFunc("GET /v1/identity", func(w http.ResponseWriter, _ *http.Request) {
		jsonOut(w, 200, map[string]any{"ak_public": m.akPub, "ek_public": m.ekPub, "release": m.rel})
	})
	mux.HandleFunc("POST /v1/configure", m.apiConfigure)
	mux.HandleFunc("GET /v1/sandboxes", func(w http.ResponseWriter, _ *http.Request) { jsonOut(w, 200, m.sandboxList()) })
	mux.HandleFunc("POST /v1/sandboxes", m.apiCreate)
	mux.HandleFunc("GET /v1/sandboxes/{id}", func(w http.ResponseWriter, r *http.Request) {
		if s := m.get(r.PathValue("id")); s != nil {
			jsonOut(w, 200, s)
			return
		}
		errOut(w, 404, fmt.Errorf("no sandbox %q", r.PathValue("id")))
	})
	mux.HandleFunc("DELETE /v1/sandboxes/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := m.removeSandbox(r.PathValue("id")); err != nil {
			errOut(w, 404, err)
			return
		}
		jsonOut(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /v1/sandboxes/{id}/{action}", m.apiAction)
	mux.HandleFunc("GET /v1/sandboxes/{id}/tunnel", m.apiTunnel)
	mux.HandleFunc("GET /v1/sandboxes/{id}/preview", m.apiPreview)
	mux.HandleFunc("GET /v1/sandboxes/{id}/key", func(w http.ResponseWriter, r *http.Request) {
		s := m.get(r.PathValue("id"))
		if s == nil {
			errOut(w, 404, fmt.Errorf("no sandbox %q", r.PathValue("id")))
			return
		}
		jsonOut(w, 200, map[string]any{"sandbox": s.ID, "openai_base_url": "http://llm.agentbox:4000/v1", "anthropic_base_url": "http://llm.agentbox:4000",
			"token": s.Token, "note": "The token only works from inside this sandbox; it is worthless anywhere else."})
	})
	mux.HandleFunc("POST /v1/permissions", m.apiPermsvc("POST", "/v1/requests"))
	mux.HandleFunc("GET /v1/permissions", m.apiPermsvc("GET", "/v1/requests"))
	mux.HandleFunc("GET /v1/permissions/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.apiPermsvc("GET", "/v1/requests/"+r.PathValue("id")+"?wait="+r.URL.Query().Get("wait"))(w, r)
	})
	mux.HandleFunc("POST /v1/recycle", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, 200, map[string]any{"ok": true})
		go func() { time.Sleep(500 * time.Millisecond); m.recycleAppliance("recycled by the user") }()
	})
	return mux
}

func (m *Manager) apiStatus(w http.ResponseWriter, _ *http.Request) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]any{
		"release": m.rel, "device_id": m.st.DeviceID, "user": m.st.User, "attestation": m.attest,
		"control_plane_error": m.cpReach,
	}
	if m.bundle != nil {
		out["bundle"] = map[string]any{"version": m.bundle.Version, "expires_at": m.bundle.ExpiresAt, "models": m.bundle.Models,
			"egress": m.bundle.Egress, "tier": m.bundle.Tier, "assurance_level": m.bundle.AssuranceLevel, "expired": time.Now().After(m.bundle.ExpiresAt)}
	}
	running := 0
	for _, s := range m.sandboxes {
		if s.State == "running" {
			running++
		}
	}
	out["sandboxes_running"] = running
	out["sandboxes_total"] = len(m.sandboxes)
	jsonOut(w, 200, out)
}

func (m *Manager) apiConfigure(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceID string `json:"device_id"`
		User     string `json:"user"`
		CARoot   string `json:"ca_root"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.DeviceID == "" || in.User == "" {
		errOut(w, 400, fmt.Errorf("device_id, user and ca_root are required"))
		return
	}
	m.mu.Lock()
	changed := m.st.DeviceID != in.DeviceID || m.st.User != in.User
	m.st.DeviceID, m.st.User, m.st.CARoot = in.DeviceID, in.User, in.CARoot
	if changed {
		m.st.Bundle, m.bundle = nil, nil
	}
	m.attest = AttestStatus{Status: "pending"}
	m.mu.Unlock()
	m.saveState()
	m.ev.SetResource("agentbox.device", in.DeviceID)
	m.ev.SetResource("agentbox.user", in.User)
	restartChild("otelcol") // pick up the device and user resource attributes
	kickAttest()
	log.Printf("configured for device %s, user %s", in.DeviceID, in.User)
	jsonOut(w, 200, map[string]any{"ok": true})
}

func (m *Manager) apiCreate(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errOut(w, 400, err)
		return
	}
	s, err := m.createSandbox(req)
	if err != nil {
		code := 400
		if s != nil {
			code = 500
		}
		errOut(w, code, err)
		return
	}
	jsonOut(w, 200, s)
}

func (m *Manager) apiAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var err error
	switch r.PathValue("action") {
	case "stop":
		err = m.stopSandbox(id, "stopped by the user", "user")
	case "start":
		err = m.startSandbox(id)
	case "recycle":
		if err = m.stopSandbox(id, "recycled by the user", "user"); err == nil {
			err = m.startSandbox(id)
		}
	default:
		err = fmt.Errorf("unknown action")
	}
	if err != nil {
		errOut(w, 400, err)
		return
	}
	jsonOut(w, 200, m.get(id))
}

func (m *Manager) apiPermsvc(method, path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
		}
		code, data, err := m.permsvc(method, path, body)
		if err != nil {
			errOut(w, 503, fmt.Errorf("permission service unreachable: %w", err))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		w.Write(data)
		if method == "POST" {
			m.nudge()
		}
	}
}

// hijack upgrades the host's request into a raw stream.
func hijack(w http.ResponseWriter) (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("cannot hijack")
	}
	c, buf, err := hj.Hijack()
	if err != nil {
		return nil, nil, err
	}
	c.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: agentbox-tunnel\r\nConnection: Upgrade\r\n\r\n"))
	return c, buf, nil
}

// apiTunnel joins a host stream to a sandbox port. Port 22 (SSH, used by
// shells and IDEs) is always allowed; other raw TCP ports need a flagged
// "port:<n>" permission in the bundle.
func (m *Manager) apiTunnel(w http.ResponseWriter, r *http.Request) {
	s := m.get(r.PathValue("id"))
	if s == nil || s.State != "running" {
		errOut(w, 404, fmt.Errorf("sandbox %q is not running", r.PathValue("id")))
		return
	}
	port, _ := strconv.Atoi(r.URL.Query().Get("port"))
	if port == 0 {
		port = 22
	}
	if port != 22 {
		b := m.currentBundle()
		allowed := false
		for _, p := range b.Ports {
			allowed = allowed || p == port
		}
		if !allowed {
			errOut(w, 403, fmt.Errorf("raw TCP tunnel to port %d needs a flagged permission: agentbox allow port:%d --reason \"...\"; use agentbox preview for web apps", port, port))
			return
		}
	}
	up, err := net.DialTimeout("tcp", net.JoinHostPort(s.IP, strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		errOut(w, 502, fmt.Errorf("sandbox port %d: %w", port, err))
		return
	}
	c, buf, err := hijack(w)
	if err != nil {
		up.Close()
		return
	}
	m.emit("tunnel.opened", map[string]any{"agentbox.sandbox": s.ID, "port": port})
	defer trackTunnel(s.ID, up)()
	splice(c, buf, up)
}

var modelPaths = regexp.MustCompile(`(?i)/(v1/)?(chat/completions|completions|responses|messages|embeddings|models)\b`)

// looksLikeModelAPI flags requests shaped like an LLM API call (D5).
func looksLikeModelAPI(r *http.Request, body []byte) bool {
	if modelPaths.MatchString(r.URL.Path) {
		return true
	}
	if r.Header.Get("x-api-key") != "" || r.Header.Get("anthropic-version") != "" || strings.HasPrefix(r.Header.Get("Authorization"), "Bearer sk-") {
		return true
	}
	b := string(body)
	return strings.Contains(b, `"messages"`) && strings.Contains(b, `"model"`)
}

// apiPreview proxies HTTP from the PC to a web app in the sandbox, request by
// request, refusing anything shaped like a model API call.
func (m *Manager) apiPreview(w http.ResponseWriter, r *http.Request) {
	s := m.get(r.PathValue("id"))
	if s == nil || s.State != "running" {
		errOut(w, 404, fmt.Errorf("sandbox %q is not running", r.PathValue("id")))
		return
	}
	port, _ := strconv.Atoi(r.URL.Query().Get("port"))
	if port <= 0 || port == 22 {
		errOut(w, 400, fmt.Errorf("preview needs a web port"))
		return
	}
	c, buf, err := hijack(w)
	if err != nil {
		return
	}
	defer c.Close()
	defer trackTunnel(s.ID, c)()
	m.emit("preview.opened", map[string]any{"agentbox.sandbox": s.ID, "port": port})
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: false}
	target := net.JoinHostPort(s.IP, strconv.Itoa(port))
	for {
		req, err := http.ReadRequest(buf.Reader)
		if err != nil {
			return
		}
		body, _ := io.ReadAll(io.LimitReader(req.Body, 10<<20))
		req.Body.Close()
		if looksLikeModelAPI(req, body) {
			m.ev.EmitSev("WARN", "preview.blocked", map[string]any{"agentbox.sandbox": s.ID, "port": port, "path": req.URL.Path, "method": req.Method})
			msg := "Agentbox: model-API-shaped traffic is not allowed through previews; this attempt was logged\n"
			resp := &http.Response{StatusCode: 403, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{"Content-Type": {"text/plain"}},
				Body: io.NopCloser(strings.NewReader(msg)), ContentLength: int64(len(msg))}
			resp.Write(c)
			continue
		}
		req.URL.Scheme, req.URL.Host, req.RequestURI = "http", target, ""
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		resp, err := tr.RoundTrip(req)
		if err != nil {
			msg := "Agentbox: preview target unreachable: " + err.Error() + "\n"
			(&http.Response{StatusCode: 502, ProtoMajor: 1, ProtoMinor: 1, Body: io.NopCloser(strings.NewReader(msg)), ContentLength: int64(len(msg))}).Write(c)
			continue
		}
		resp.Write(c)
		resp.Body.Close()
	}
}
