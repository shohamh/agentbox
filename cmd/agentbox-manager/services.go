package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	"github.com/miekg/dns"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	pb "gvisor.dev/gvisor/pkg/sentry/seccheck/points/points_go_proto"

	"agentbox/pkg/bundle"
	"agentbox/pkg/extauthz"
)

// ---------- local Envoy ext_authz: sandbox token -> appliance identity ----------

type authz struct {
	authv3.UnimplementedAuthorizationServer
	m *Manager
}

type bucket struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

func (b *bucket) take(rpm int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if b.last.IsZero() {
		b.tokens = float64(rpm)
	}
	b.tokens += now.Sub(b.last).Minutes() * float64(rpm)
	if b.tokens > float64(rpm) {
		b.tokens = float64(rpm)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (m *Manager) serveAuthz() {
	lis, err := net.Listen("tcp", "127.0.0.1:9001")
	if err != nil {
		log.Fatal(err)
	}
	g := grpc.NewServer()
	authv3.RegisterAuthorizationServer(g, &authz{m: m})
	log.Fatal(g.Serve(lis))
}

func bearer(h map[string]string) string {
	if v := h["authorization"]; v != "" {
		if t, ok := strings.CutPrefix(v, "Bearer "); ok {
			return strings.TrimSpace(t)
		}
		return strings.TrimSpace(v)
	}
	return strings.TrimSpace(h["x-api-key"])
}

func (a *authz) Check(ctx context.Context, req *authv3.CheckRequest) (*authv3.CheckResponse, error) {
	m := a.m
	src := req.GetAttributes().GetSource().GetAddress().GetSocketAddress().GetAddress()
	h := req.GetAttributes().GetRequest().GetHttp().GetHeaders()
	tok := bearer(h)
	m.mu.RLock()
	sbID := m.byIP[src]
	owner := m.tokens[tok]
	att := m.attest
	m.mu.RUnlock()
	if owner != "" && owner != sbID {
		// D4: a sandbox token presented from somewhere other than its sandbox.
		m.ev.EmitSev("WARN", "token.relay", map[string]any{"agentbox.sandbox": owner, "source_ip": src, "source_sandbox": sbID, "path": h[":path"]})
		return extauthz.Deny(403, "token_relay", "Agentbox: this sandbox token only works from its own sandbox; the attempt was logged"), nil
	}
	if sbID == "" {
		return extauthz.Deny(403, "unknown_source", "Agentbox: requests must come from a sandbox"), nil
	}
	if owner != sbID {
		return extauthz.Deny(401, "invalid_token", "Agentbox: missing or wrong sandbox token (use $AGENTBOX_TOKEN)"), nil
	}
	if att.Status != "pass" {
		return extauthz.Deny(503, "not_attested", "Agentbox: model access is paused - appliance attestation "+att.Status+": "+att.Reason), nil
	}
	b, why := m.bundleValid()
	if why != "" {
		return extauthz.Deny(503, "policy_unavailable", "Agentbox: model service unreachable - "+why), nil
	}
	if gatewayDown() {
		return extauthz.Deny(503, "model_service_unreachable", "Agentbox: model service unreachable - this PC cannot reach the control plane right now. Your sandboxes keep running; model calls resume automatically when it is back."), nil
	}
	m.mu.Lock()
	bk := m.rate[sbID]
	if bk == nil {
		bk = &bucket{}
		m.rate[sbID] = bk
	}
	m.mu.Unlock()
	rpm := b.RateLimitRPM
	if rpm <= 0 {
		rpm = 120
	}
	if !bk.take(rpm) {
		return extauthz.Deny(429, "rate_limited", fmt.Sprintf("Agentbox: sandbox rate limit of %d requests per minute reached", rpm)), nil
	}
	s := m.get(sbID)
	return extauthz.Allow(map[string]string{
		"x-agentbox-sandbox": sbID,
		"x-agentbox-image":   s.Image,
		"x-agentbox-bundle":  strconv.FormatInt(b.Version, 10),
	}, "authorization", "x-api-key"), nil
}

// ---------- egress proxy (HTTP CONNECT and plain HTTP) ----------

func (m *Manager) sandboxFor(remote string) *Sandbox {
	host, _, _ := net.SplitHostPort(remote)
	m.mu.RLock()
	id := m.byIP[host]
	m.mu.RUnlock()
	if id == "" {
		return nil
	}
	return m.get(id)
}

func (m *Manager) egressAllowed(s *Sandbox, host string) (bool, string) {
	b, why := m.bundleValid()
	if why != "" {
		return false, why
	}
	if bundle.EgressAllowed(b.Egress, host) {
		return true, ""
	}
	return false, "not in this sandbox's egress policy"
}

func (m *Manager) serveEgress() {
	srv := &http.Server{Addr: "10.88.0.1:3128", Handler: http.HandlerFunc(m.egress), ReadHeaderTimeout: 15 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func (m *Manager) egress(w http.ResponseWriter, r *http.Request) {
	s := m.sandboxFor(r.RemoteAddr)
	if s == nil {
		http.Error(w, "Agentbox: unknown source", 403)
		return
	}
	host := r.URL.Hostname()
	port := r.URL.Port()
	if r.Method == http.MethodConnect {
		host, port, _ = net.SplitHostPort(r.Host)
	}
	if port == "" {
		port = "80"
	}
	ok, why := m.egressAllowed(s, host)
	attrs := map[string]any{"agentbox.sandbox": s.ID, "host": host, "port": port, "method": r.Method}
	if !ok {
		attrs["reason"] = why
		m.ev.EmitSev("WARN", "egress.denied", attrs)
		msg := fmt.Sprintf("Agentbox: egress to %s is blocked (%s). Request it with: agentbox allow egress:%s --reason \"...\"\n", host, why, host)
		http.Error(w, msg, http.StatusForbidden)
		return
	}
	m.emit("egress.allowed", attrs)
	target := net.JoinHostPort(host, port)
	if r.Method == http.MethodConnect {
		up, err := net.DialTimeout("tcp", target, 10*time.Second)
		if err != nil {
			http.Error(w, "Agentbox: upstream unreachable: "+err.Error(), 502)
			return
		}
		hj, _ := w.(http.Hijacker)
		c, buf, err := hj.Hijack()
		if err != nil {
			up.Close()
			return
		}
		c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		splice(c, buf, up)
		return
	}
	rp := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.Out.URL = &url.URL{Scheme: "http", Host: target, Path: r.URL.Path, RawQuery: r.URL.RawQuery}
		pr.Out.Host = r.Host
		pr.Out.Header.Del("Proxy-Connection")
	}, Transport: &http.Transport{Proxy: nil}}
	rp.ServeHTTP(w, r)
}

func splice(c net.Conn, buf *bufio.ReadWriter, up net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		if buf != nil && buf.Reader.Buffered() > 0 {
			b, _ := buf.Reader.Peek(buf.Reader.Buffered())
			up.Write(b)
		}
		io.Copy(up, c)
		if cw, ok := up.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		io.Copy(c, up)
		if cw, ok := c.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
	c.Close()
	up.Close()
}

// ---------- DNS responder: only allowlisted names resolve ----------

func (m *Manager) serveDNS() {
	h := dns.HandlerFunc(m.dns)
	go func() { log.Fatal((&dns.Server{Addr: "10.88.0.1:53", Net: "tcp", Handler: h}).ListenAndServe()) }()
	log.Fatal((&dns.Server{Addr: "10.88.0.1:53", Net: "udp", Handler: h}).ListenAndServe())
}

var internalNames = map[string]bool{"llm.agentbox.": true, "otel.agentbox.": true, "agentbox.": true}

func (m *Manager) dns(w dns.ResponseWriter, r *dns.Msg) {
	resp := new(dns.Msg)
	resp.SetReply(r)
	resp.Authoritative = true
	s := m.sandboxFor(w.RemoteAddr().String())
	for _, q := range r.Question {
		name := strings.ToLower(q.Name)
		if internalNames[name] {
			if q.Qtype == dns.TypeA {
				resp.Answer = append(resp.Answer, &dns.A{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("10.88.0.1")})
			}
			continue
		}
		if s != nil {
			if ok, _ := m.egressAllowed(s, strings.TrimSuffix(name, ".")); ok {
				if q.Qtype == dns.TypeA {
					ips, _ := net.LookupIP(strings.TrimSuffix(name, "."))
					for _, ip := range ips {
						if v4 := ip.To4(); v4 != nil {
							resp.Answer = append(resp.Answer, &dns.A{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30}, A: v4})
						}
					}
				}
				continue
			}
			m.emit("dns.denied", map[string]any{"agentbox.sandbox": s.ID, "name": name})
		}
		resp.Rcode = dns.RcodeNameError
	}
	w.WriteMsg(resp)
}

// ---------- OTLP from sandboxes, stamped with the sandbox it came from ----------

func (m *Manager) serveOTLPProxy() {
	target, _ := url.Parse("http://127.0.0.1:4318")
	rp := httputil.NewSingleHostReverseProxy(target)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := m.sandboxFor(r.RemoteAddr)
		if s == nil {
			http.Error(w, "unknown source", 403)
			return
		}
		r.Header.Set("X-Agentbox-Sandbox", s.ID)
		rp.ServeHTTP(w, r)
	})
	log.Fatal((&http.Server{Addr: "10.88.0.1:4318", Handler: h, ReadHeaderTimeout: 15 * time.Second}).ListenAndServe())
}

// ---------- gVisor runtime monitoring sink ----------

const wireVersion = 1

func (m *Manager) serveTrace() {
	path := filepath.Join(m.cfg.RunDir, "trace.sock")
	os.Remove(path)
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_SEQPACKET, 0)
	if err != nil {
		log.Fatalf("trace socket: %v", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrUnix{Name: path}); err != nil {
		log.Fatalf("trace bind: %v", err)
	}
	if err := unix.Listen(fd, 64); err != nil {
		log.Fatalf("trace listen: %v", err)
	}
	log.Printf("gVisor trace sink listening on %s", path)
	for {
		nfd, _, err := unix.Accept(fd)
		if err != nil {
			log.Printf("trace accept: %v", err)
			continue
		}
		go m.traceConn(nfd)
	}
}

func (m *Manager) traceConn(fd int) {
	defer unix.Close(fd)
	buf := make([]byte, 1<<20)
	n, err := unix.Read(fd, buf)
	if err != nil {
		return
	}
	var hs pb.Handshake
	if err := proto.Unmarshal(buf[:n], &hs); err != nil {
		log.Printf("trace handshake: %v", err)
		return
	}
	out, _ := proto.Marshal(&pb.Handshake{Version: wireVersion})
	if _, err := unix.Write(fd, out); err != nil {
		return
	}
	for {
		n, err := unix.Read(fd, buf)
		if err != nil || n == 0 {
			return
		}
		if n < 8 {
			continue
		}
		hdrSize := int(binary.LittleEndian.Uint16(buf[0:2]))
		typ := pb.MessageType(binary.LittleEndian.Uint16(buf[2:4]))
		if hdrSize > n {
			continue
		}
		m.tracePoint(typ, buf[hdrSize:n])
	}
}

func (m *Manager) tracePoint(typ pb.MessageType, payload []byte) {
	switch typ {
	case pb.MessageType_MESSAGE_SENTRY_EXEC:
		var e pb.ExecveInfo
		if proto.Unmarshal(payload, &e) != nil {
			return
		}
		cd := e.GetContextData()
		cmd := strings.Join(e.GetArgv(), " ")
		if len(cmd) > 1024 {
			cmd = cmd[:1024]
		}
		m.emit("process.exec", map[string]any{
			"agentbox.sandbox": cd.GetContainerId(), "process.executable": e.GetBinaryPath(), "process.command_line": cmd,
			"process.pid": int64(cd.GetThreadGroupId()), "process.parent_pid": int64(cd.GetParentThreadGroupId()), "process.cwd": cd.GetCwd(),
		})
	case pb.MessageType_MESSAGE_SYSCALL_CONNECT:
		var c pb.Connect
		if proto.Unmarshal(payload, &c) != nil {
			return
		}
		addr, fam := sockaddr(c.GetAddress())
		if fam == "unix" || addr == "" || strings.HasPrefix(addr, "127.") {
			return
		}
		res := int64(0)
		if c.GetExit() != nil {
			res = c.GetExit().GetResult()
		}
		cd := c.GetContextData()
		m.emit("net.connect", map[string]any{"agentbox.sandbox": cd.GetContainerId(), "net.peer": addr, "process.name": cd.GetProcessName(), "process.pid": int64(cd.GetThreadGroupId()), "result": res})
	}
}

// sockaddr decodes a raw sockaddr (as captured by gVisor) into host:port.
func sockaddr(b []byte) (string, string) {
	if len(b) < 2 {
		return "", ""
	}
	switch binary.LittleEndian.Uint16(b[0:2]) {
	case unix.AF_INET:
		if len(b) < 8 {
			return "", "inet"
		}
		port := binary.BigEndian.Uint16(b[2:4])
		return net.JoinHostPort(net.IP(b[4:8]).String(), strconv.Itoa(int(port))), "inet"
	case unix.AF_INET6:
		if len(b) < 24 {
			return "", "inet6"
		}
		port := binary.BigEndian.Uint16(b[2:4])
		return net.JoinHostPort(net.IP(b[8:24]).String(), strconv.Itoa(int(port))), "inet6"
	case unix.AF_UNIX:
		return "", "unix"
	}
	return "", "other"
}

// ---------- gateway reachability ----------

// gwFails counts consecutive failed probes of the front gateway. A request
// sent on an already-open connection to a blackholed peer would otherwise hang
// until TCP gives up, so the model endpoint answers with a clear error while
// the gateway is known to be down.
var gwFails atomic.Int32

func gatewayDown() bool { return gwFails.Load() >= 2 }

func (m *Manager) gatewayProbe() {
	port := m.cfg.GatewayPort
	if port == 0 {
		port = 443
	}
	addr := net.JoinHostPort(m.cfg.GatewayHost, strconv.Itoa(port))
	for {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			if n := gwFails.Add(1); n == 2 {
				log.Printf("front gateway %s unreachable: %v; model calls fail fast until it is back", addr, err)
				m.ev.EmitSev("WARN", "gateway.unreachable", map[string]any{"addr": addr, "error": err.Error()})
			}
		} else {
			c.Close()
			if gwFails.Swap(0) >= 2 {
				log.Printf("front gateway %s reachable again", addr)
				m.emit("gateway.reachable", map[string]any{"addr": addr})
			}
		}
		time.Sleep(2 * time.Second)
	}
}
