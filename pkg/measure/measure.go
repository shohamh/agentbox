// Package measure stands in for systemd-stub's measurement of the unified
// kernel image: at "boot" it hashes the appliance's measured file set in a
// fixed order and extends each into PCR 11. The release build computes the
// same values to produce the signed release manifest.
package measure

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"agentbox/pkg/tpm"
)

// ListFile names the measured files, one path per line. It is measured first.
const ListFile = "/etc/agentbox/measured-files"

type Entry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Version   string    `json:"version"`
	Flavor    string    `json:"flavor"`
	Created   time.Time `json:"created"`
	PCR11     string    `json:"pcr11"`
	Entries   []Entry   `json:"entries"`
	Signature string    `json:"signature,omitempty"`
}

// Payload is the canonical bytes the release key signs.
func (m Manifest) Payload() []byte {
	m.Signature = ""
	b, _ := json.Marshal(m)
	return b
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Collect hashes the list file and every file it names. root prefixes paths
// (empty in the appliance itself).
func Collect(root, flavor string) ([]Entry, error) {
	f, err := os.Open(root + ListFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	paths := []string{ListFile}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l != "" && !strings.HasPrefix(l, "#") {
			paths = append(paths, l)
		}
	}
	var out []Entry
	for _, p := range paths {
		h, err := hashFile(root + p)
		if err != nil {
			return nil, fmt.Errorf("measure %s: %w", p, err)
		}
		out = append(out, Entry{Path: p, SHA256: h})
	}
	// The flavor is part of the measured command line: dev never matches prod.
	out = append(out, Entry{Path: "flavor", SHA256: hex.EncodeToString(sha(flavor))})
	return out, nil
}

func sha(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }

func Digests(entries []Entry) [][32]byte {
	d := make([][32]byte, 0, len(entries))
	for _, e := range entries {
		d = append(d, tpm.MeasureDigest(e.Path, e.SHA256))
	}
	return d
}

func ExpectedPCR(entries []Entry) string {
	return hex.EncodeToString(tpm.Replay(Digests(entries)))
}

// Diff lists entries whose hash differs from, or is missing in, the manifest.
func Diff(manifest, log []Entry) []string {
	want := map[string]string{}
	for _, e := range manifest {
		want[e.Path] = e.SHA256
	}
	var out []string
	seen := map[string]bool{}
	for _, e := range log {
		seen[e.Path] = true
		if w, ok := want[e.Path]; !ok {
			out = append(out, e.Path+": not in release manifest")
		} else if w != e.SHA256 {
			out = append(out, e.Path+": hash differs from release manifest")
		}
	}
	for _, e := range manifest {
		if !seen[e.Path] {
			out = append(out, e.Path+": missing")
		}
	}
	return out
}
