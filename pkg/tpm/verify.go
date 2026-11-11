package tpm

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"

	"github.com/google/go-tpm/tpm2"
)

// AKKey converts a marshalled TPM2B_PUBLIC (ECC P-256) into a Go key.
func AKKey(pub []byte) (*ecdsa.PublicKey, error) {
	p2b, err := tpm2.Unmarshal[tpm2.TPM2BPublic](pub)
	if err != nil {
		return nil, fmt.Errorf("AK public: %w", err)
	}
	p, err := p2b.Contents()
	if err != nil {
		return nil, err
	}
	if p.Type != tpm2.TPMAlgECC || !p.ObjectAttributes.Restricted || !p.ObjectAttributes.FixedTPM {
		return nil, errors.New("AK must be a restricted, fixedTPM ECC key")
	}
	pt, err := p.Unique.ECC()
	if err != nil {
		return nil, err
	}
	return &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(pt.X.Buffer), Y: new(big.Int).SetBytes(pt.Y.Buffer)}, nil
}

// VerifyQuote checks the signature with the registered AK, the nonce, and
// that the quoted PCR digest matches the reported PCR 11 value. It returns
// the verified PCR 11 value.
func VerifyQuote(q *Quote, registeredAK []byte, nonce []byte) ([]byte, error) {
	if !bytes.Equal(q.AKPublic, registeredAK) {
		return nil, errors.New("quote signed by a key other than the enrolled attestation key")
	}
	key, err := AKKey(registeredAK)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(q.Quoted)
	if !ecdsa.Verify(key, digest[:], new(big.Int).SetBytes(q.SigR), new(big.Int).SetBytes(q.SigS)) {
		return nil, errors.New("quote signature invalid")
	}
	att, err := tpm2.Unmarshal[tpm2.TPMSAttest](q.Quoted)
	if err != nil {
		return nil, fmt.Errorf("attest: %w", err)
	}
	if att.Magic != tpm2.TPMGeneratedValue || att.Type != tpm2.TPMSTAttestQuote {
		return nil, errors.New("not a TPM-generated quote")
	}
	if !bytes.Equal(att.ExtraData.Buffer, nonce) {
		return nil, errors.New("nonce mismatch (replayed quote)")
	}
	info, err := att.Attested.Quote()
	if err != nil {
		return nil, err
	}
	want := sha256.Sum256(q.PCRValue)
	if !bytes.Equal(info.PCRDigest.Buffer, want[:]) {
		return nil, errors.New("reported PCR 11 does not match the quoted digest")
	}
	return q.PCRValue, nil
}
