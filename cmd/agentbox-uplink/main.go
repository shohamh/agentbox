//go:build linux

// agentbox-uplink runs in the appliance VM. It exposes the control-plane ports
// on 127.0.0.2 and carries each connection to the host over vsock (CID 2, the
// same port number); the host service relays it to the control plane. The
// appliance resolves the *.agentbox.corp names to 127.0.0.2, so TLS names and
// ports are unchanged.
package main

import (
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mdlayher/vsock"
)

func main() {
	ports := "443,8443,4318,5000,18443"
	if v := os.Getenv("AGENTBOX_UPLINK_PORTS"); v != "" {
		ports = v
	}
	for _, p := range strings.Split(ports, ",") {
		port, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			log.Fatalf("bad port %q", p)
		}
		go serve(port)
	}
	select {}
}

func serve(port int) {
	addr := net.JoinHostPort("127.0.0.2", strconv.Itoa(port))
	var l net.Listener
	var err error
	for {
		if l, err = net.Listen("tcp", addr); err == nil {
			break
		}
		log.Printf("listen %s: %v (retrying)", addr, err)
		time.Sleep(2 * time.Second)
	}
	log.Printf("uplink %s -> host vsock port %d", addr, port)
	for {
		c, err := l.Accept()
		if err != nil {
			log.Printf("accept %s: %v", addr, err)
			time.Sleep(time.Second)
			continue
		}
		go func() {
			defer c.Close()
			up, err := vsock.Dial(vsock.Host, uint32(port), nil)
			if err != nil {
				log.Printf("vsock dial host:%d: %v", port, err)
				return
			}
			defer up.Close()
			done := make(chan struct{}, 2)
			go func() { io.Copy(up, c); done <- struct{}{} }()
			go func() { io.Copy(c, up); done <- struct{}{} }()
			<-done
		}()
	}
}
