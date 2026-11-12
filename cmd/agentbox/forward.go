package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"strconv"
)

// forward opens a raw TCP tunnel from a local port on this PC to a port in a
// sandbox (a database, a debug port, an SSH to something running inside). Unlike
// preview, which only carries web traffic, this needs the port permission:
// `agentbox allow port:<n> --reason "..."` (flagged: applied at once, reviewed).
func forward(args []string) error {
	fs := flag.NewFlagSet("forward", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:0", "local address")
	var pos []string
	for len(args) > 0 {
		fs.Parse(args)
		args = fs.Args()
		if len(args) > 0 {
			pos = append(pos, args[0])
			args = args[1:]
		}
	}
	if len(pos) != 2 {
		return fmt.Errorf("usage: agentbox forward <sandbox> <port> [--listen addr]")
	}
	if n, err := strconv.Atoi(pos[1]); err != nil || n <= 0 || n > 65535 {
		return fmt.Errorf("port must be a number from 1 to 65535")
	}
	path := fmt.Sprintf("/v1/sandboxes/%s/tunnel?port=%s", pos[0], pos[1])
	// Open one tunnel now, so a missing permission is reported before anything listens.
	probe, err := cl.Tunnel(path)
	if err != nil {
		return err
	}
	probe.Close()
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	fmt.Printf("Forwarding %s to port %s of %s (Ctrl-C to stop)\n", l.Addr(), pos[1], pos[0])
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		go func() {
			t, err := cl.Tunnel(path)
			if err != nil {
				c.Close()
				return
			}
			go func() { io.Copy(t, c); t.Close() }()
			io.Copy(c, t)
			c.Close()
		}()
	}
}
