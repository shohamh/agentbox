// mkbundle writes an appliance state.json holding a signed policy bundle, so an
// appliance can be exercised without the control plane (image tests only).
//
//	mkbundle -key policy.key -device pc-test -user alice -egress git.agentbox.corp \
//	         -image base=registry.agentbox.corp:5000/agentbox/base@sha256:... > state.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"agentbox/pkg/bundle"
)

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(s string) error { *l = append(*l, s); return nil }

func main() {
	key := flag.String("key", "", "policy private key file")
	device := flag.String("device", "pc-test", "device ID")
	user := flag.String("user", "alice", "user")
	var egress, images list
	flag.Var(&egress, "egress", "allowed egress host (repeatable)")
	flag.Var(&images, "image", "name=ref@sha256:digest[,harness] (repeatable)")
	flag.Parse()
	priv, err := bundle.LoadPrivateKey(*key)
	if err != nil {
		log.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	b := &bundle.Bundle{DeviceID: *device, User: *user, Group: "default", Version: 1, IssuedAt: now, ExpiresAt: now.Add(24 * time.Hour),
		Tier: 1, AssuranceLevel: "C", Models: []string{"deepseek-v3", "glm-4"}, Egress: egress, Images: map[string]bundle.ImageRef{},
		Limits: bundle.Limits{CPUs: 2, MemoryMB: 2048, PIDs: 512}, RateLimitRPM: 120, ReleaseChannel: "broad"}
	for _, im := range images {
		name, rest, ok := strings.Cut(im, "=")
		ref, digest, ok2 := strings.Cut(rest, "@")
		if !ok || !ok2 {
			log.Fatalf("bad -image %q", im)
		}
		harness := ""
		if d, h, found := strings.Cut(digest, ","); found {
			digest, harness = d, h
		}
		b.Images[name] = bundle.ImageRef{Ref: ref, Digest: digest, Harness: harness}
	}
	s, err := bundle.Sign(b, priv)
	if err != nil {
		log.Fatal(err)
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"device_id": *device, "user": *user, "ca_root": "", "bundle": s})
	fmt.Fprintln(os.Stderr, "bundle v1 signed for", *device)
}
