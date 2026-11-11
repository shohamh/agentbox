// Package bundle defines the signed per-device policy bundle: everything a
// sandbox on one appliance may do. permsvc is the only writer; the manager and
// host service are the only enforcers.
package bundle

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Bundle is the policy for one device. It is serialized to JSON and signed.
type Bundle struct {
	DeviceID       string              `json:"device_id"`
	User           string              `json:"user"`
	Group          string              `json:"group"`
	Version        int64               `json:"version"`
	IssuedAt       time.Time           `json:"issued_at"`
	ExpiresAt      time.Time           `json:"expires_at"`
	Tier           int                 `json:"tier"`
	AssuranceLevel string              `json:"assurance_level"`
	Models         []string            `json:"models"`
	Egress         []string            `json:"egress"`
	Images         map[string]ImageRef `json:"images"`
	Mounts         []string            `json:"mounts"`
	Ports          []int               `json:"ports"`
	Limits         Limits              `json:"limits"`
	RateLimitRPM   int                 `json:"rate_limit_rpm"`
	ReleaseChannel string              `json:"release_channel"`
	Grants         []Grant             `json:"grants"`
}

type ImageRef struct {
	Ref     string `json:"ref"`
	Digest  string `json:"digest"`
	Harness string `json:"harness,omitempty"`
}

type Limits struct {
	CPUs     int `json:"cpus"`
	MemoryMB int `json:"memory_mb"`
	PIDs     int `json:"pids"`
}

type Grant struct {
	ID         string     `json:"id"`
	Permission string     `json:"permission"`
	Class      string     `json:"class"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// Signed is the wire form: the exact payload bytes plus an Ed25519 signature.
type Signed struct {
	Payload   string `json:"payload"` // base64 of the bundle JSON
	KeyID     string `json:"key_id"`
	Signature string `json:"signature"` // base64
}

func Sign(b *Bundle, priv ed25519.PrivateKey) (*Signed, error) {
	payload, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(priv, payload)
	return &Signed{
		Payload:   base64.StdEncoding.EncodeToString(payload),
		KeyID:     KeyID(priv.Public().(ed25519.PublicKey)),
		Signature: base64.StdEncoding.EncodeToString(sig),
	}, nil
}

var (
	ErrSignature = errors.New("bundle signature invalid")
	ErrDevice    = errors.New("bundle is for another device")
	ErrRollback  = errors.New("bundle version is not newer than the current one")
	ErrExpired   = errors.New("bundle has expired")
)

// Verify checks signature, device, monotonic version and expiry.
// minVersion is the version currently applied; the new one must be greater.
func Verify(s *Signed, pub ed25519.PublicKey, deviceID string, minVersion int64, now time.Time) (*Bundle, error) {
	payload, err := base64.StdEncoding.DecodeString(s.Payload)
	if err != nil {
		return nil, ErrSignature
	}
	sig, err := base64.StdEncoding.DecodeString(s.Signature)
	if err != nil || !ed25519.Verify(pub, payload, sig) {
		return nil, ErrSignature
	}
	var b Bundle
	if err := json.Unmarshal(payload, &b); err != nil {
		return nil, fmt.Errorf("bundle payload: %w", err)
	}
	if b.DeviceID != deviceID {
		return nil, ErrDevice
	}
	if b.Version <= minVersion {
		return nil, ErrRollback
	}
	if now.After(b.ExpiresAt) {
		return nil, ErrExpired
	}
	return &b, nil
}

// Decode returns the payload without verifying it (for display only).
func Decode(s *Signed) (*Bundle, error) {
	payload, err := base64.StdEncoding.DecodeString(s.Payload)
	if err != nil {
		return nil, err
	}
	var b Bundle
	return &b, json.Unmarshal(payload, &b)
}

func KeyID(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:8])
}

// EgressAllowed reports whether host (without port) matches the allowlist.
// Patterns: exact host, or "*.example.com" for subdomains.
func EgressAllowed(allow []string, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, p := range allow {
		p = strings.ToLower(p)
		if p == host {
			return true
		}
		if strings.HasPrefix(p, "*.") && strings.HasSuffix(host, p[1:]) {
			return true
		}
	}
	return false
}

// Key files are stored as base64 of the raw key bytes.

func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%s: not an ed25519 private key", path)
	}
	return ed25519.PrivateKey(raw), nil
}

func LoadPublicKey(path string) (ed25519.PublicKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%s: not an ed25519 public key", path)
	}
	return ed25519.PublicKey(raw), nil
}

// SignBlob / VerifyBlob are used for release manifests.
func SignBlob(priv ed25519.PrivateKey, data []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data))
}

func VerifyBlob(pub ed25519.PublicKey, data []byte, sigB64 string) bool {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
	return err == nil && ed25519.Verify(pub, data, sig)
}
