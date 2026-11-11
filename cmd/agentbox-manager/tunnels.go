package main

import (
	"net"
	"sync"
)

// Open tunnels and previews per sandbox. When a sandbox is stopped (by the
// user, an admin, or policy) its tunnels are closed at once, so shells and
// agent tasks attached to it end immediately instead of waiting for TCP to
// give up on a peer that no longer exists.
var (
	tunMu   sync.Mutex
	tunnels = map[string]map[net.Conn]struct{}{}
)

func trackTunnel(sandboxID string, c net.Conn) (untrack func()) {
	tunMu.Lock()
	if tunnels[sandboxID] == nil {
		tunnels[sandboxID] = map[net.Conn]struct{}{}
	}
	tunnels[sandboxID][c] = struct{}{}
	tunMu.Unlock()
	return func() {
		tunMu.Lock()
		delete(tunnels[sandboxID], c)
		tunMu.Unlock()
	}
}

func closeTunnels(sandboxID string) int {
	tunMu.Lock()
	conns := tunnels[sandboxID]
	delete(tunnels, sandboxID)
	tunMu.Unlock()
	for c := range conns {
		c.Close()
	}
	return len(conns)
}

// lifecycleMu serializes sandbox start and stop so that, for example, the
// automatic restore after an appliance reboot and a user's `agentbox start`
// for the same sandbox cannot run runsc twice at once.
var lifecycleMu sync.Mutex
