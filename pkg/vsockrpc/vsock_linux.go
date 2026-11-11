//go:build linux

package vsockrpc

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/mdlayher/vsock"
)

func listenVsock(rest string) (net.Listener, error) {
	_, p, _ := strings.Cut(rest, ":")
	port, err := strconv.ParseUint(p, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("vsock port: %w", err)
	}
	return vsock.Listen(uint32(port), nil)
}

func dialVsock(ctx context.Context, scheme, rest string) (net.Conn, error) {
	if scheme != "vsock" {
		return nil, fmt.Errorf("%s is only available on Windows", scheme)
	}
	c, p, _ := strings.Cut(rest, ":")
	cid, err := strconv.ParseUint(c, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("vsock cid: %w", err)
	}
	port, err := strconv.ParseUint(p, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("vsock port: %w", err)
	}
	return vsock.Dial(uint32(cid), uint32(port), nil)
}
