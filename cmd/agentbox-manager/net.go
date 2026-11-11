package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

const bridge = "abx0"

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func nsName(id string) string { return "abx-" + id }

func vethName(id string) string { return "v" + strings.TrimPrefix(id, "sb-") }

// setupNetwork creates the sandbox bridge and the nftables policy: sandboxes
// may reach only the model endpoint, egress proxy, DNS responder and OTLP
// endpoint on the appliance; nothing is forwarded anywhere else.
func (m *Manager) setupNetwork() error {
	if err := run("ip", "link", "show", bridge); err != nil {
		if err := run("ip", "link", "add", bridge, "type", "bridge"); err != nil {
			return err
		}
	}
	run("ip", "addr", "add", "10.88.0.1/24", "dev", bridge)
	if err := run("ip", "link", "set", bridge, "up"); err != nil {
		return err
	}
	os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("0\n"), 0o644)
	ruleset := `
table inet agentbox
delete table inet agentbox
table inet agentbox {
  chain input {
    type filter hook input priority 0; policy accept;
    iifname "abx0" ct state established,related accept
    iifname "abx0" ip daddr 10.88.0.1 tcp dport { 4000, 3128, 4318, 53 } accept
    iifname "abx0" ip daddr 10.88.0.1 udp dport 53 accept
    iifname "abx0" icmp type echo-request accept
    iifname "abx0" log prefix "abx-drop-in " limit rate 10/second drop
    iifname "abx0" drop
  }
  chain forward {
    type filter hook forward priority 0; policy accept;
    iifname "abx0" log prefix "abx-drop-fwd " limit rate 10/second drop
    oifname "abx0" drop
  }
}
`
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(ruleset)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft: %v: %s", err, out)
	}
	log.Printf("sandbox bridge %s 10.88.0.1/24 with nftables policy ready", bridge)
	return nil
}

func (m *Manager) sandboxNet(s *Sandbox) error {
	ns := nsName(s.ID)
	v := vethName(s.ID)
	m.teardownNet(s)
	steps := [][]string{
		{"ip", "netns", "add", ns},
		{"ip", "link", "add", v, "type", "veth", "peer", "name", "eth0", "netns", ns},
		{"ip", "link", "set", v, "master", bridge, "up"},
		{"ip", "-n", ns, "addr", "add", s.IP + "/24", "dev", "eth0"},
		{"ip", "-n", ns, "link", "set", "eth0", "up"},
		{"ip", "-n", ns, "link", "set", "lo", "up"},
		{"ip", "-n", ns, "route", "add", "default", "via", "10.88.0.1"},
	}
	for _, st := range steps {
		if err := run(st[0], st[1:]...); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) teardownNet(s *Sandbox) {
	exec.Command("ip", "link", "del", vethName(s.ID)).Run()
	exec.Command("ip", "netns", "del", nsName(s.ID)).Run()
}
