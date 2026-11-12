package tpm

import (
	"fmt"
	"time"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
)

// ProbeResult is the outcome of one TPM operation, reported by `agentbox
// doctor` so a TPM that behaves differently (for example the Hyper-V virtual
// TPM against the swtpm used in tests) is diagnosed in one run.
type ProbeResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Ms     int64  `json:"ms"`
}

func timed(name string, f func() (string, error)) ProbeResult {
	t0 := time.Now()
	d, err := f()
	r := ProbeResult{Name: name, Ms: time.Since(t0).Milliseconds(), Detail: d}
	if err != nil {
		r.Detail = err.Error()
	} else {
		r.OK = true
	}
	return r
}

func createPrimary(t transport.TPM, hier tpm2.TPMHandle, tmpl tpm2.TPMTPublic) (string, error) {
	rsp, err := tpm2.CreatePrimary{PrimaryHandle: hier, InPublic: tpm2.New2B(tmpl)}.Execute(t)
	if err != nil {
		return "", err
	}
	tpm2.FlushContext{FlushHandle: rsp.ObjectHandle}.Execute(t)
	return "created and flushed", nil
}

// Probe tries a series of TPM operations on a fresh connection and reports each.
func Probe(t transport.TPM) []ProbeResult {
	var out []ProbeResult
	out = append(out, timed("read PCR 11", func() (string, error) {
		p, err := ReadPCR(t)
		return fmt.Sprintf("%x", p), err
	}))
	out = append(out, timed("capability: manufacturer and firmware", func() (string, error) {
		rsp, err := tpm2.GetCapability{Capability: tpm2.TPMCapTPMProperties, Property: uint32(tpm2.TPMPTManufacturer), PropertyCount: 1}.Execute(t)
		if err != nil {
			return "", err
		}
		props, err := rsp.CapabilityData.Data.TPMProperties()
		if err != nil || len(props.TPMProperty) == 0 {
			return "", fmt.Errorf("no properties: %v", err)
		}
		v := props.TPMProperty[0].Value
		return fmt.Sprintf("manufacturer %c%c%c%c", byte(v>>24), byte(v>>16), byte(v>>8), byte(v)), nil
	}))
	out = append(out, timed("ECC P-256 attestation key under the owner hierarchy", func() (string, error) {
		return createPrimary(t, tpm2.TPMRHOwner, akTemplate())
	}))
	out = append(out, timed("ECC P-256 attestation key under the endorsement hierarchy", func() (string, error) {
		return createPrimary(t, tpm2.TPMRHEndorsement, akTemplate())
	}))
	out = append(out, timed("ECC storage key (SRK) under the owner hierarchy", func() (string, error) {
		return createPrimary(t, tpm2.TPMRHOwner, tpm2.ECCSRKTemplate)
	}))
	out = append(out, timed("RSA storage key (SRK) under the owner hierarchy", func() (string, error) {
		return createPrimary(t, tpm2.TPMRHOwner, tpm2.RSASRKTemplate)
	}))
	out = append(out, timed("ECC endorsement key", func() (string, error) {
		return createPrimary(t, tpm2.TPMRHEndorsement, tpm2.ECCEKTemplate)
	}))
	out = append(out, timed("RSA endorsement key", func() (string, error) {
		return createPrimary(t, tpm2.TPMRHEndorsement, tpm2.RSAEKTemplate)
	}))
	return out
}
