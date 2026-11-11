// Package identity parses and builds Agentbox SPIFFE-style identities and
// verifies peers from mutual-TLS certificates.
package identity

import (
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const TrustDomain = "agentbox.corp"

type Appliance struct {
	Device string
	User   string
}

func ApplianceID(device, user string) string {
	return fmt.Sprintf("spiffe://%s/appliance/%s/%s", TrustDomain, device, user)
}

func DeviceID(device, user string) string {
	return fmt.Sprintf("spiffe://%s/device/%s/%s", TrustDomain, device, user)
}

// Parse accepts spiffe://agentbox.corp/{appliance|device}/<device>/<user>.
func Parse(id string) (kind string, a Appliance, err error) {
	u, err := url.Parse(id)
	if err != nil || u.Scheme != "spiffe" || u.Host != TrustDomain {
		return "", a, fmt.Errorf("not an %s identity: %q", TrustDomain, id)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 3 || (parts[0] != "appliance" && parts[0] != "device") || parts[1] == "" || parts[2] == "" {
		return "", a, fmt.Errorf("malformed identity %q", id)
	}
	return parts[0], Appliance{Device: parts[1], User: parts[2]}, nil
}

// FromCert extracts the Agentbox identity from a verified client certificate.
func FromCert(c *x509.Certificate) (string, Appliance, error) {
	for _, u := range c.URIs {
		if u.Scheme == "spiffe" {
			return Parse(u.String())
		}
	}
	return "", Appliance{}, fmt.Errorf("certificate has no spiffe URI")
}

// FromRequest returns the appliance identity of an mTLS request.
func FromRequest(r *http.Request) (Appliance, error) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
		return Appliance{}, fmt.Errorf("client certificate required")
	}
	kind, a, err := FromCert(r.TLS.VerifiedChains[0][0])
	if err != nil {
		return a, err
	}
	if kind != "appliance" {
		return a, fmt.Errorf("an appliance identity is required, got %s", kind)
	}
	return a, nil
}
