// Package vsockrpc is the host-to-appliance channel. In production it runs
// over vsock (vhost-vsock on Linux, Hyper-V sockets on Windows); for the
// container test bed it runs over TCP on a private network shared only by the
// PC and its appliance. The protocol on top is HTTP/1.1 with JSON bodies, plus
// connection upgrades for SSH and preview tunnels.
package vsockrpc

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// Listen accepts "tcp://host:port" or "vsock://:port".
func Listen(addr string) (net.Listener, error) {
	scheme, rest, ok := strings.Cut(addr, "://")
	if !ok {
		return nil, fmt.Errorf("address %q needs a scheme", addr)
	}
	switch scheme {
	case "tcp":
		return net.Listen("tcp", rest)
	case "vsock":
		return listenVsock(rest)
	}
	return nil, fmt.Errorf("unsupported scheme %q", scheme)
}

// Dial accepts "tcp://host:port", "vsock://cid:port" or "hvsock://vmid:port".
func Dial(ctx context.Context, addr string) (net.Conn, error) {
	scheme, rest, ok := strings.Cut(addr, "://")
	if !ok {
		return nil, fmt.Errorf("address %q needs a scheme", addr)
	}
	switch scheme {
	case "tcp":
		var d net.Dialer
		return d.DialContext(ctx, "tcp", rest)
	case "vsock", "hvsock":
		return dialVsock(ctx, scheme, rest)
	}
	return nil, fmt.Errorf("unsupported scheme %q", scheme)
}

// Client returns an HTTP client whose every connection goes over the channel.
// Requests use the placeholder host "appliance".
func Client(addr string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return Dial(ctx, addr) },
			MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second,
		},
	}
}
