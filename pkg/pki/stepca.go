// Package pki talks to step-ca: it mints one-time tokens with the JWK
// provisioner key and signs CSRs for device and appliance certificates.
package pki

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"go.step.sm/crypto/jose"
)

type CAClient struct {
	URL         string
	RootPEM     []byte
	Pool        *x509.CertPool
	fingerprint string
	provisioner string
	password    []byte
	http        *http.Client

	mu  sync.Mutex
	key *jose.JSONWebKey
}

func NewCAClient(caURL, rootFile, provisioner, passwordFile string) (*CAClient, error) {
	rootPEM, err := os.ReadFile(rootFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(rootPEM) {
		return nil, fmt.Errorf("%s: no certificates", rootFile)
	}
	blk, _ := pem.Decode(rootPEM)
	fp := sha256.Sum256(blk.Bytes)
	pw, err := os.ReadFile(passwordFile)
	if err != nil {
		return nil, err
	}
	return &CAClient{
		URL: strings.TrimRight(caURL, "/"), RootPEM: rootPEM, Pool: pool,
		fingerprint: hex.EncodeToString(fp[:]), provisioner: provisioner,
		password: bytes.TrimSpace(pw),
		http:     &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}},
	}, nil
}

func (c *CAClient) loadKey() (*jose.JSONWebKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key != nil {
		return c.key, nil
	}
	resp, err := c.http.Get(c.URL + "/provisioners?limit=100")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Provisioners []struct {
			Type         string `json:"type"`
			Name         string `json:"name"`
			EncryptedKey string `json:"encryptedKey"`
		} `json:"provisioners"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("provisioners: %w", err)
	}
	for _, p := range out.Provisioners {
		if p.Type == "JWK" && p.Name == c.provisioner {
			raw, err := jose.Decrypt([]byte(p.EncryptedKey), jose.WithPassword(c.password))
			if err != nil {
				return nil, fmt.Errorf("decrypt provisioner key: %w", err)
			}
			var k jose.JSONWebKey
			if err := json.Unmarshal(raw, &k); err != nil {
				return nil, err
			}
			c.key = &k
			return c.key, nil
		}
	}
	return nil, fmt.Errorf("JWK provisioner %q not found", c.provisioner)
}

func (c *CAClient) token(subject string, sans []string) (string, error) {
	k, err := c.loadKey()
	if err != nil {
		return "", err
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: k.Key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", k.KeyID))
	if err != nil {
		return "", err
	}
	jti := make([]byte, 16)
	rand.Read(jti)
	now := time.Now()
	claims := map[string]any{
		"iss":  c.provisioner,
		"aud":  c.URL + "/1.0/sign",
		"sub":  subject,
		"sans": sans,
		"sha":  c.fingerprint,
		"iat":  now.Unix(),
		"nbf":  now.Add(-time.Minute).Unix(),
		"exp":  now.Add(5 * time.Minute).Unix(),
		"jti":  hex.EncodeToString(jti),
	}
	payload, _ := json.Marshal(claims)
	sig, err := signer.Sign(payload)
	if err != nil {
		return "", err
	}
	return sig.CompactSerialize()
}

// Sign submits a CSR and returns the leaf and chain PEM.
func (c *CAClient) Sign(csrPEM []byte, subject string, sans []string, validity time.Duration) ([]byte, error) {
	ott, err := c.token(subject, sans)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{
		"csr":      string(csrPEM),
		"ott":      ott,
		"notAfter": validity.String(),
	})
	resp, err := c.http.Post(c.URL+"/1.0/sign", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("step-ca sign: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var out struct {
		CertChain []string `json:"certChain"`
		Crt       string   `json:"crt"`
		CA        string   `json:"ca"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if len(out.CertChain) > 0 {
		return []byte(strings.Join(out.CertChain, "")), nil
	}
	return []byte(out.Crt + out.CA), nil
}

// NewKeyAndCSR creates an ECDSA P-256 key and a CSR with the given SANs.
func NewKeyAndCSR(cn string, sans []string) (keyPEM, csrPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return CSRForKey(key, cn, sans)
}

func CSRForKey(key crypto.Signer, cn string, sans []string) (keyPEM, csrPEM []byte, err error) {
	tmpl := &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}
	for _, s := range sans {
		if u, err := url.Parse(s); err == nil && u.Scheme != "" {
			tmpl.URIs = append(tmpl.URIs, u)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, s)
		}
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return nil, nil, err
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// LeafNotAfter parses the first certificate in a PEM chain.
func LeafNotAfter(chain []byte) (time.Time, error) {
	blk, _ := pem.Decode(chain)
	if blk == nil {
		return time.Time{}, fmt.Errorf("no PEM block")
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return time.Time{}, err
	}
	return c.NotAfter, nil
}

// ServerTLS builds a TLS config for a control-plane server; when clientCAs is
// non-nil, client certificates are requested and verified if presented.
func ServerTLS(certFile, keyFile string, clientCAs *x509.CertPool, require bool) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	if clientCAs != nil {
		cfg.ClientCAs = clientCAs
		cfg.ClientAuth = tls.VerifyClientCertIfGiven
		if require {
			cfg.ClientAuth = tls.RequireAndVerifyClientCert
		}
	}
	return cfg, nil
}

func LoadPool(file string) (*x509.CertPool, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("%s: no certificates", file)
	}
	return p, nil
}
