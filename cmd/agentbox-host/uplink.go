package main

import (
	"io"
	"log"
	"net"
	"strconv"
	"time"

	"agentbox/pkg/vsockrpc"
)

// The appliance reaches the control plane through the host over the same Hyper-V
// socket (vsock) channel it is managed over: no virtual-switch address, no
// Windows Firewall rule, nothing listening on the network. For each port the
// host accepts guest connections on that vsock port and relays them to the
// control plane, which runs on this PC (Docker Desktop in the test setup).
// In a datacenter the appliance has a routed uplink instead and this is off.
var defaultUplinkPorts = []int{443, 8443, 4318, 5000, 18443}

func (c Config) uplinkPorts() []int {
	if c.Driver == "container" {
		return nil
	}
	if c.UplinkPorts != nil {
		return c.UplinkPorts
	}
	return defaultUplinkPorts
}

func (c Config) uplinkTarget() string {
	if c.UplinkTarget != "" {
		return c.UplinkTarget
	}
	return "127.0.0.1"
}

func (h *host) startUplink() {
	for _, p := range h.cfg.uplinkPorts() {
		go h.relay(p)
	}
}

func (h *host) relay(port int) {
	target := net.JoinHostPort(h.cfg.uplinkTarget(), strconv.Itoa(port))
	for {
		l, err := vsockrpc.ListenGuests(uint32(port))
		if err != nil {
			// The service GUID is registered when the VM is created; keep trying.
			log.Printf("uplink %d: %v (retrying in 10s)", port, err)
			time.Sleep(10 * time.Second)
			continue
		}
		log.Printf("uplink: guest vsock port %d -> %s", port, target)
		for {
			c, err := l.Accept()
			if err != nil {
				log.Printf("uplink %d: accept: %v", port, err)
				l.Close()
				break
			}
			go func() {
				defer c.Close()
				up, err := net.DialTimeout("tcp", target, 5*time.Second)
				if err != nil {
					log.Printf("uplink %d: %s: %v", port, target, err)
					return
				}
				defer up.Close()
				done := make(chan struct{}, 2)
				go func() { io.Copy(up, c); done <- struct{}{} }()
				go func() { io.Copy(c, up); done <- struct{}{} }()
				<-done
			}()
		}
		time.Sleep(2 * time.Second)
	}
}
