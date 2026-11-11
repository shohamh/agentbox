//go:build windows

package hostapi

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

func dial(ctx context.Context, addr string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, addr)
}

// Listen creates the named pipe; only SYSTEM and interactive users may connect.
func Listen(addr string) (net.Listener, error) {
	return winio.ListenPipe(addr, &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;SY)(A;;GRGW;;;IU)"})
}
