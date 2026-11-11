// Package hostapi is the local channel between the agentbox CLI and the
// agentbox-host service: a Unix socket on Linux, a named pipe on Windows,
// restricted to the logged-in user.
package hostapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

func DefaultAddress() string {
	if v := os.Getenv("AGENTBOX_HOST_SOCKET"); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		return `\\.\pipe\agentbox-host`
	}
	return "/run/agentbox/host.sock"
}

type Client struct {
	Addr string
	HTTP *http.Client
}

func New(addr string) *Client {
	c := &Client{Addr: addr}
	c.HTTP = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dial(ctx, addr)
	}}}
	return c
}

type APIError struct {
	Code int
	Msg  string
}

func (e *APIError) Error() string { return e.Msg }

func (c *Client) Do(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, "http://agentbox-host"+path, body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach the agentbox host service (%s): %w\nIs it running? On Linux: systemctl status agentbox-host; on Windows: Get-Service agentbox-host", c.Addr, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return &APIError{Code: resp.StatusCode, Msg: e.Error}
		}
		return &APIError{Code: resp.StatusCode, Msg: strings.TrimSpace(string(data))}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Upgrade opens a raw stream for tunnels: the server answers 101 and the
// connection becomes a byte pipe.
func Upgrade(conn net.Conn, path string) (net.Conn, error) {
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: agentbox\r\nConnection: Upgrade\r\nUpgrade: agentbox-tunnel\r\n\r\n", path)
	br := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetReadDeadline(time.Time{})
	if resp.StatusCode != http.StatusSwitchingProtocols {
		b, _ := io.ReadAll(resp.Body)
		conn.Close()
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return nil, fmt.Errorf("%s", e.Error)
		}
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return &bufConn{Conn: conn, r: br}, nil
}

func (c *Client) Tunnel(path string) (net.Conn, error) {
	conn, err := dial(context.Background(), c.Addr)
	if err != nil {
		return nil, err
	}
	return Upgrade(conn, path)
}

type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

func (b *bufConn) CloseWrite() error {
	if cw, ok := b.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}
