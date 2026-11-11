package tpm

import (
	"os"
	"testing"
)

// Runs against a real (software) TPM: TPM_SOCKET=/run/swtpm/tpm.sock go test ./pkg/tpm
// (swtpm socket --tpm2 --server type=unixio,path=... --flags not-need-init,startup-clear).
func TestQuoteRoundTrip(t *testing.T) {
	sock := os.Getenv("TPM_SOCKET")
	if sock == "" {
		t.Skip("TPM_SOCKET not set")
	}
	tp, err := OpenSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer tp.Close()
	ak, err := LoadAK(tp)
	if err != nil {
		t.Fatal(err)
	}
	ak2, err := LoadAK(tp)
	if err != nil {
		t.Fatal(err)
	}
	if string(ak.Public) != string(ak2.Public) {
		t.Fatal("AK must be stable across loads (derived from the endorsement seed)")
	}
	if _, err := EKPublic(tp); err != nil {
		t.Fatal(err)
	}
	var digests [][32]byte
	for _, p := range []string{"/usr/bin/a", "/usr/bin/b", "/etc/c"} {
		d := MeasureDigest(p, "deadbeef")
		digests = append(digests, d)
		if err := Extend(tp, d); err != nil {
			t.Fatal(err)
		}
	}
	nonce := []byte("0123456789abcdef0123456789abcdef")
	q, err := ak.Quote(nonce)
	if err != nil {
		t.Fatal(err)
	}
	pcr, err := VerifyQuote(q, ak.Public, nonce)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if string(pcr) != string(Replay(digests)) {
		t.Fatalf("PCR 11 %x does not equal the replayed event log %x", pcr, Replay(digests))
	}
	// T7: a replayed quote under a different nonce must fail.
	if _, err := VerifyQuote(q, ak.Public, []byte("another-nonce-another-nonce-12345")); err == nil {
		t.Fatal("replayed quote accepted")
	}
	// A quote from an unregistered key must fail.
	if _, err := VerifyQuote(q, []byte("not-the-registered-key"), nonce); err == nil {
		t.Fatal("quote accepted for the wrong AK")
	}
	// Forged PCR value must fail.
	q2 := *q
	q2.PCRValue = make([]byte, 32)
	if _, err := VerifyQuote(&q2, ak.Public, nonce); err == nil {
		t.Fatal("forged PCR value accepted")
	}
}
