package bundle

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func TestSignVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now()
	b := &Bundle{DeviceID: "pc1", User: "alice", Version: 5, ExpiresAt: now.Add(time.Hour), Egress: []string{"git.agentbox.corp"}}
	s, err := Sign(b, priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(s, pub, "pc1", 4, now); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := Verify(s, pub, "pc2", 4, now); err != ErrDevice {
		t.Fatalf("want ErrDevice, got %v", err)
	}
	if _, err := Verify(s, pub, "pc1", 5, now); err != ErrRollback {
		t.Fatalf("want ErrRollback, got %v", err)
	}
	if _, err := Verify(s, pub, "pc1", 4, now.Add(2*time.Hour)); err != ErrExpired {
		t.Fatalf("want ErrExpired, got %v", err)
	}
	// T12: edit the payload on disk -> signature fails.
	s2 := *s
	s2.Payload = s.Payload[:len(s.Payload)-4] + "AAAA"
	if _, err := Verify(&s2, pub, "pc1", 4, now); err != ErrSignature {
		t.Fatalf("want ErrSignature, got %v", err)
	}
}

func TestEgressAllowed(t *testing.T) {
	allow := []string{"git.agentbox.corp", "*.pypi.internal"}
	cases := map[string]bool{
		"git.agentbox.corp":      true,
		"GIT.agentbox.corp.":     true,
		"files.pypi.internal":    true,
		"pypi.internal":          false,
		"evil.com":               false,
		"git.agentbox.corp.evil": false,
	}
	for h, want := range cases {
		if got := EgressAllowed(allow, h); got != want {
			t.Errorf("%s: got %v want %v", h, got, want)
		}
	}
}
