// agentbox-measure stands in for systemd-stub in the container test bed.
//
//	agentbox-measure boot      extend each measured file into PCR 11 of the
//	                           virtual TPM and write the event log
//	agentbox-measure manifest  print the unsigned release manifest for this
//	                           image (run at release build time)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"agentbox/pkg/measure"
	"agentbox/pkg/tpm"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: agentbox-measure boot|manifest [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	sock := fs.String("tpm", "/run/swtpm/tpm.sock", "virtual TPM socket")
	out := fs.String("log", "/run/agentbox/eventlog.json", "event log path")
	root := fs.String("root", "", "filesystem root prefix")
	version := fs.String("version", "", "release version")
	fs.Parse(os.Args[2:])
	var rel struct{ Version, Flavor string }
	if b, err := os.ReadFile(*root + "/etc/agentbox/release.json"); err == nil {
		json.Unmarshal(b, &rel)
	}
	if *version == "" {
		*version = rel.Version
	}
	entries, err := measure.Collect(*root, rel.Flavor)
	if err != nil {
		log.Fatal(err)
	}
	switch os.Args[1] {
	case "boot":
		t, err := tpm.Open(*sock)
		if err != nil {
			log.Fatal(err)
		}
		for _, d := range measure.Digests(entries) {
			if err := tpm.Extend(t, d); err != nil {
				log.Fatalf("extend: %v", err)
			}
		}
		pcr, _ := tpm.ReadPCR(t)
		b, _ := json.MarshalIndent(entries, "", "  ")
		os.MkdirAll("/run/agentbox", 0o755)
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("measured %d entries into PCR 11 = %x (flavor %s)\n", len(entries), pcr, rel.Flavor)
	case "manifest":
		m := measure.Manifest{Version: *version, Flavor: rel.Flavor, Created: time.Now().UTC(), PCR11: measure.ExpectedPCR(entries), Entries: entries}
		json.NewEncoder(os.Stdout).Encode(m)
	default:
		log.Fatalf("unknown command %s", os.Args[1])
	}
}
