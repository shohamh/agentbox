//go:build !windows

package hostapi

import (
	"context"
	"net"
	"os"
	"path/filepath"
)

func dial(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", addr)
}

// Listen creates the socket; mode 0660 with the agentbox group in production.
func Listen(addr string) (net.Listener, error) {
	os.MkdirAll(filepath.Dir(addr), 0o755)
	os.Remove(addr)
	l, err := net.Listen("unix", addr)
	if err != nil {
		return nil, err
	}
	os.Chmod(addr, 0o660)
	return l, nil
}
