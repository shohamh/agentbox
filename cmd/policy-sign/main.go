// policy-sign manages Agentbox signing keys and signs release manifests.
//
//	policy-sign keygen <dir>                   create policy and release keys if absent
//	policy-sign pub <keyfile>                  print the public key for a private key file
//	policy-sign manifest <release-key> <out>   sign the manifest JSON read from stdin
//
// In production the release key lives on an offline HSM and is used in a
// two-person ceremony; the policy root delegates to an online key in Vault.
// The proof of concept keeps both as files in a secrets volume.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"agentbox/pkg/bundle"
	"agentbox/pkg/measure"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: policy-sign keygen <dir> | pub <keyfile> | manifest <release-key> <out>")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "keygen":
		dir := os.Args[2]
		os.MkdirAll(dir, 0o700)
		for _, name := range []string{"policy", "release"} {
			priv := filepath.Join(dir, name+".key")
			if _, err := os.Stat(priv); err == nil {
				fmt.Printf("%s exists\n", priv)
				continue
			}
			pub, key, _ := ed25519.GenerateKey(rand.Reader)
			must(os.WriteFile(priv, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600))
			must(os.WriteFile(filepath.Join(dir, name+".pub"), []byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0o644))
			fmt.Printf("created %s (key id %s)\n", priv, bundle.KeyID(pub))
		}
	case "pub":
		k, err := bundle.LoadPrivateKey(os.Args[2])
		must(err)
		fmt.Println(base64.StdEncoding.EncodeToString(k.Public().(ed25519.PublicKey)))
	case "manifest":
		if len(os.Args) < 4 {
			log.Fatal("manifest <release-key> <out>")
		}
		k, err := bundle.LoadPrivateKey(os.Args[2])
		must(err)
		data, err := io.ReadAll(os.Stdin)
		must(err)
		var m measure.Manifest
		must(json.Unmarshal(data, &m))
		m.Signature = bundle.SignBlob(k, m.Payload())
		b, _ := json.MarshalIndent(m, "", "  ")
		must(os.WriteFile(os.Args[3], b, 0o644))
		fmt.Printf("signed release %s (%s) pcr11=%s -> %s\n", m.Version, m.Flavor, m.PCR11, os.Args[3])
	default:
		log.Fatalf("unknown command %s", os.Args[1])
	}
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
