// Package tpm is the appliance's light attestation: an attestation key in the
// (virtual) TPM, PCR 11 measurement of the appliance image, and quotes over a
// server nonce. Verification lives in verify.go and runs on the control plane.
package tpm

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
)

const PCR = 11

// SocketTPM speaks raw TPM 2.0 commands to swtpm over a unix socket
// (swtpm socket --server type=unixio,path=...).
type SocketTPM struct {
	mu   sync.Mutex
	path string
	conn net.Conn
}

var _ transport.TPMCloser = (*SocketTPM)(nil)

func OpenSocket(path string) (*SocketTPM, error) {
	s := &SocketTPM{path: path}
	var err error
	for i := 0; i < 50; i++ {
		if s.conn, err = net.Dial("unix", path); err == nil {
			return s, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, fmt.Errorf("open TPM %s: %w", path, err)
}

func (s *SocketTPM) Send(cmd []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		c, err := net.Dial("unix", s.path)
		if err != nil {
			return nil, err
		}
		s.conn = c
	}
	s.conn.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := s.conn.Write(cmd); err != nil {
		s.conn.Close()
		s.conn = nil
		return nil, err
	}
	hdr := make([]byte, 10)
	if _, err := io.ReadFull(s.conn, hdr); err != nil {
		s.conn.Close()
		s.conn = nil
		return nil, err
	}
	size := binary.BigEndian.Uint32(hdr[2:6])
	if size < 10 || size > 1<<20 {
		return nil, fmt.Errorf("bad TPM response size %d", size)
	}
	rest := make([]byte, size-10)
	if _, err := io.ReadFull(s.conn, rest); err != nil {
		s.conn.Close()
		s.conn = nil
		return nil, err
	}
	return append(hdr, rest...), nil
}

func (s *SocketTPM) Close() error {
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}

func akTemplate() tpm2.TPMTPublic {
	return tpm2.TPMTPublic{
		Type:    tpm2.TPMAlgECC,
		NameAlg: tpm2.TPMAlgSHA256,
		ObjectAttributes: tpm2.TPMAObject{
			FixedTPM: true, FixedParent: true, SensitiveDataOrigin: true,
			UserWithAuth: true, Restricted: true, SignEncrypt: true, NoDA: true,
		},
		Parameters: tpm2.NewTPMUPublicParms(tpm2.TPMAlgECC, &tpm2.TPMSECCParms{
			Scheme: tpm2.TPMTECCScheme{
				Scheme:  tpm2.TPMAlgECDSA,
				Details: tpm2.NewTPMUAsymScheme(tpm2.TPMAlgECDSA, &tpm2.TPMSSigSchemeECDSA{HashAlg: tpm2.TPMAlgSHA256}),
			},
			CurveID: tpm2.TPMECCNistP256,
		}),
	}
}

// AK is a loaded attestation key. Being a primary key derived from the
// endorsement seed, it is identical across appliance reboots as long as the
// virtual TPM state persists.
type AK struct {
	tpm    transport.TPM
	handle tpm2.TPMHandle
	name   tpm2.TPM2BName
	Public []byte // marshalled TPM2B_PUBLIC
}

func LoadAK(t transport.TPM) (*AK, error) {
	// The owner hierarchy first: it needs no endorsement authorization, which
	// some virtual TPMs restrict. The AK only has to be a restricted signing key
	// that never leaves the TPM; the enrollment registers its public part.
	var errs []string
	for _, h := range []struct {
		name string
		hier tpm2.TPMHandle
	}{{"owner", tpm2.TPMRHOwner}, {"endorsement", tpm2.TPMRHEndorsement}} {
		rsp, err := tpm2.CreatePrimary{PrimaryHandle: h.hier, InPublic: tpm2.New2B(akTemplate())}.Execute(t)
		if err != nil {
			errs = append(errs, h.name+": "+err.Error())
			continue
		}
		return &AK{tpm: t, handle: rsp.ObjectHandle, name: rsp.Name, Public: tpm2.Marshal(rsp.OutPublic)}, nil
	}
	return nil, fmt.Errorf("create AK: %s", strings.Join(errs, "; "))
}

// EKPublic returns the marshalled public part of the ECC endorsement key.
func EKPublic(t transport.TPM) ([]byte, error) {
	rsp, err := tpm2.CreatePrimary{
		PrimaryHandle: tpm2.TPMRHEndorsement,
		InPublic:      tpm2.New2B(tpm2.ECCEKTemplate),
	}.Execute(t)
	if err != nil {
		return nil, fmt.Errorf("create EK: %w", err)
	}
	defer tpm2.FlushContext{FlushHandle: rsp.ObjectHandle}.Execute(t)
	return tpm2.Marshal(rsp.OutPublic), nil
}

func sel() tpm2.TPMLPCRSelection {
	return tpm2.TPMLPCRSelection{PCRSelections: []tpm2.TPMSPCRSelection{{
		Hash: tpm2.TPMAlgSHA256, PCRSelect: tpm2.PCClientCompatible.PCRs(PCR),
	}}}
}

func Extend(t transport.TPM, digest [32]byte) error {
	_, err := tpm2.PCRExtend{
		PCRHandle: tpm2.AuthHandle{Handle: tpm2.TPMHandle(PCR), Auth: tpm2.PasswordAuth(nil)},
		Digests:   tpm2.TPMLDigestValues{Digests: []tpm2.TPMTHA{{HashAlg: tpm2.TPMAlgSHA256, Digest: digest[:]}}},
	}.Execute(t)
	return err
}

func ReadPCR(t transport.TPM) ([]byte, error) {
	rsp, err := tpm2.PCRRead{PCRSelectionIn: sel()}.Execute(t)
	if err != nil {
		return nil, err
	}
	if len(rsp.PCRValues.Digests) != 1 {
		return nil, fmt.Errorf("PCR read returned %d values", len(rsp.PCRValues.Digests))
	}
	return rsp.PCRValues.Digests[0].Buffer, nil
}

// Quote is the evidence the appliance sends.
type Quote struct {
	Quoted    []byte `json:"quoted"` // TPMS_ATTEST
	SigR      []byte `json:"sig_r"`
	SigS      []byte `json:"sig_s"`
	PCRValue  []byte `json:"pcr11"`
	AKPublic  []byte `json:"ak_public"`
}

func (a *AK) Quote(nonce []byte) (*Quote, error) {
	rsp, err := tpm2.Quote{
		SignHandle:     tpm2.AuthHandle{Handle: a.handle, Name: a.name, Auth: tpm2.PasswordAuth(nil)},
		QualifyingData: tpm2.TPM2BData{Buffer: nonce},
		InScheme: tpm2.TPMTSigScheme{
			Scheme:  tpm2.TPMAlgECDSA,
			Details: tpm2.NewTPMUSigScheme(tpm2.TPMAlgECDSA, &tpm2.TPMSSchemeHash{HashAlg: tpm2.TPMAlgSHA256}),
		},
		PCRSelect: sel(),
	}.Execute(a.tpm)
	if err != nil {
		return nil, fmt.Errorf("quote: %w", err)
	}
	ecc, err := rsp.Signature.Signature.ECDSA()
	if err != nil {
		return nil, err
	}
	pcr, err := ReadPCR(a.tpm)
	if err != nil {
		return nil, err
	}
	return &Quote{Quoted: rsp.Quoted.Bytes(), SigR: ecc.SignatureR.Buffer, SigS: ecc.SignatureS.Buffer, PCRValue: pcr, AKPublic: a.Public}, nil
}

// MeasureDigest is what one measured file contributes to PCR 11.
func MeasureDigest(path string, fileSHA256 string) [32]byte {
	return sha256.Sum256([]byte("agentbox-measure:v1\x00" + path + "\x00" + fileSHA256))
}

// Replay computes the PCR value produced by extending, in order, from zero.
func Replay(digests [][32]byte) []byte {
	pcr := make([]byte, 32)
	for _, d := range digests {
		h := sha256.New()
		h.Write(pcr)
		h.Write(d[:])
		pcr = h.Sum(nil)
	}
	return pcr
}

// Conn is an open TPM, a swtpm socket or a kernel device.
type Conn = transport.TPMCloser

// DeviceTPM speaks raw TPM 2.0 commands to a kernel device such as
// /dev/tpmrm0 (the Hyper-V virtual TPM, or swtpm behind QEMU's tpm-crb).
// The device takes one whole command per write and answers one whole
// response per read.
type DeviceTPM struct {
	mu sync.Mutex
	f  *os.File
}

func openDevice(path string) (*DeviceTPM, error) {
	var err error
	var f *os.File
	for i := 0; i < 50; i++ {
		if f, err = openBlocking(path); err == nil {
			return &DeviceTPM{f: f}, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, fmt.Errorf("open TPM device %s: %w", path, err)
}

func (d *DeviceTPM) Send(cmd []byte) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.f.Write(cmd); err != nil {
		return nil, err
	}
	buf := make([]byte, 8192)
	n, err := d.f.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func (d *DeviceTPM) Close() error { return d.f.Close() }

// Open connects to path: a character device (/dev/tpmrm0) or a swtpm unix socket.
func Open(path string) (Conn, error) {
	// Device nodes appear when the driver loads: wait for them rather than
	// falling back to a unix-socket dial.
	if strings.HasPrefix(path, "/dev/") {
		return openDevice(path)
	}
	if st, err := os.Stat(path); err == nil && st.Mode()&os.ModeDevice != 0 {
		return openDevice(path)
	}
	return OpenSocket(path)
}
