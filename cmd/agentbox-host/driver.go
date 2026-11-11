package main

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"agentbox/pkg/vsockrpc"
)

// Check is one prerequisite reported by `agentbox status`.
type Check struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Info string `json:"info,omitempty"`
	Fix  string `json:"fix,omitempty"`
}

// Driver owns the appliance VM on this PC.
type Driver interface {
	Name() string
	Checks() []Check
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	// ChannelAddr is the host-to-appliance address (vsock, hvsock or tcp).
	ChannelAddr() string
}

func newDriver(cfg Config) (Driver, error) {
	switch cfg.Driver {
	case "container":
		return &containerDriver{addr: cfg.ApplianceAddr}, nil
	case "qemu":
		return newQEMU(cfg)
	case "hyperv":
		return newHyperV(cfg)
	}
	return nil, fmt.Errorf("unknown driver %q (container, qemu, hyperv)", cfg.Driver)
}

// containerDriver is the test-bed driver: the appliance runs as a sibling
// container started by Compose, reachable over a private network that stands
// in for vsock. Start and stop are the container runtime's job.
type containerDriver struct{ addr string }

func (d *containerDriver) Name() string        { return "container" }
func (d *containerDriver) ChannelAddr() string { return d.addr }

func (d *containerDriver) Checks() []Check {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := vsockrpc.Dial(ctx, d.addr)
	ok := err == nil
	if c != nil {
		c.Close()
	}
	ch := Check{Name: "appliance channel", OK: ok, Info: d.addr}
	if !ok {
		ch.Fix = "start the appliance container (docker compose up -d appliance-…)"
	}
	return []Check{{Name: "platform", OK: true, Info: runtime.GOOS + " (container test bed)"}, ch}
}

func (d *containerDriver) Start(context.Context) error { return nil }
func (d *containerDriver) Stop(context.Context) error {
	return fmt.Errorf("in the container test bed the appliance is stopped with docker compose stop")
}
