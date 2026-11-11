// certinit issues the control plane's server certificates from step-ca
// (JWK provisioner) into a shared volume, then exits. Idempotent: certificates
// with more than ten days left are kept.
//
//	certinit -ca-url https://ca.agentbox.corp:9000 -root /stepca/certs/root_ca.crt \
//	  -password-file /secrets/stepca_password -out /pki \
//	  -cert enroll=enroll.agentbox.corp -cert gw=gw.agentbox.corp,front-envoy
package main

import (
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agentbox/pkg/pki"
)

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	caURL := flag.String("ca-url", "https://ca.agentbox.corp:9000", "step-ca URL")
	root := flag.String("root", "/stepca/certs/root_ca.crt", "root CA certificate")
	prov := flag.String("provisioner", "agentbox", "JWK provisioner name")
	pw := flag.String("password-file", "/secrets/stepca_password", "provisioner password file")
	out := flag.String("out", "/pki", "output directory")
	valid := flag.Duration("valid", 30*24*time.Hour, "certificate lifetime")
	var certs multi
	flag.Var(&certs, "cert", "name=dns1,dns2 (repeatable)")
	flag.Parse()

	var ca *pki.CAClient
	var err error
	for i := 0; i < 90; i++ {
		if ca, err = pki.NewCAClient(*caURL, *root, *prov, *pw); err == nil {
			if _, err = ca.Sign(nil, "probe", nil, time.Hour); err != nil && strings.Contains(err.Error(), "dial") {
				time.Sleep(2 * time.Second)
				continue
			}
			break
		}
		log.Printf("waiting for step-ca: %v", err)
		time.Sleep(2 * time.Second)
	}
	if ca == nil {
		log.Fatalf("step-ca client: %v", err)
	}
	os.MkdirAll(*out, 0o755)
	rootPEM, _ := os.ReadFile(*root)
	if err := os.WriteFile(filepath.Join(*out, "ca.crt"), rootPEM, 0o644); err != nil {
		log.Fatal(err)
	}
	for _, c := range certs {
		name, list, ok := strings.Cut(c, "=")
		if !ok {
			log.Fatalf("bad -cert %q", c)
		}
		sans := strings.Split(list, ",")
		crt, key := filepath.Join(*out, name+".crt"), filepath.Join(*out, name+".key")
		if b, err := os.ReadFile(crt); err == nil {
			if na, err := pki.LeafNotAfter(b); err == nil && time.Until(na) > 10*24*time.Hour {
				if _, err := os.Stat(key); err == nil && covers(b, sans) {
					log.Printf("%s: certificate still valid until %s", name, na.Format(time.RFC3339))
					continue
				}
			}
		}
		var chain []byte
		for attempt := 0; attempt < 30; attempt++ {
			keyPEM, csr, err := pki.NewKeyAndCSR(sans[0], sans)
			if err != nil {
				log.Fatal(err)
			}
			if chain, err = ca.Sign(csr, sans[0], sans, *valid); err != nil {
				log.Printf("%s: %v (retrying)", name, err)
				time.Sleep(2 * time.Second)
				continue
			}
			if err := os.WriteFile(key, keyPEM, 0o644); err != nil { // test bed: readable by service users
				log.Fatal(err)
			}
			break
		}
		if chain == nil {
			log.Fatalf("%s: could not obtain a certificate", name)
		}
		if err := os.WriteFile(crt, chain, 0o644); err != nil {
			log.Fatal(err)
		}
		log.Printf("%s: issued for %s", name, strings.Join(sans, ", "))
	}
	fmt.Println("certinit done")
}

func covers(chain []byte, sans []string) bool {
	blk, _ := pem.Decode(chain)
	if blk == nil {
		return false
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return false
	}
	have := map[string]bool{}
	for _, d := range c.DNSNames {
		have[d] = true
	}
	for _, s := range sans {
		if !have[s] {
			return false
		}
	}
	return true
}
