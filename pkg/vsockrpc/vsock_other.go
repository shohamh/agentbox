//go:build !linux && !windows

package vsockrpc

import (
	"context"
	"fmt"
	"net"
)

func listenVsock(string) (net.Listener, error) { return nil, fmt.Errorf("vsock unsupported") }

func dialVsock(context.Context, string, string) (net.Conn, error) {
	return nil, fmt.Errorf("vsock unsupported")
}
