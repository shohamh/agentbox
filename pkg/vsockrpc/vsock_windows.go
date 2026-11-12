//go:build windows

package vsockrpc

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
)

// VsockServiceID maps a vsock port to the Hyper-V socket service GUID that
// Linux guests see as that port (the documented VSOCK template GUID).
func VsockServiceID(port uint32) guid.GUID {
	g, _ := guid.FromString("00000000-facb-11e6-bd58-64006a7986d3")
	g.Data1 = port
	return g
}

func listenVsock(string) (net.Listener, error) {
	return nil, fmt.Errorf("the host does not listen on vsock")
}

// dialVsock handles hvsock://<vm-guid>:<port>.
func dialVsock(ctx context.Context, scheme, rest string) (net.Conn, error) {
	if scheme != "hvsock" {
		return nil, fmt.Errorf("use hvsock://<vm-id>:<port> on Windows")
	}
	vm, p, _ := strings.Cut(rest, ":")
	vmID, err := guid.FromString(vm)
	if err != nil {
		return nil, fmt.Errorf("vm id: %w", err)
	}
	port, err := strconv.ParseUint(p, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("port: %w", err)
	}
	return winio.Dial(ctx, &winio.HvsockAddr{VMID: vmID, ServiceID: VsockServiceID(uint32(port))})
}

// ListenGuests accepts connections made by guest VMs to this host's Hyper-V
// socket service for the given vsock port (the Linux guest dials CID 2).
func ListenGuests(port uint32) (net.Listener, error) {
	return winio.ListenHvsock(&winio.HvsockAddr{VMID: winio.HvsockGUIDWildcard(), ServiceID: VsockServiceID(port)})
}
