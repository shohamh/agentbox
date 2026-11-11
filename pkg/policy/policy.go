// Package policy holds group defaults and catalogs, turns them plus a user's
// grants into a device bundle, and classifies permission requests with OPA.
package policy

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/open-policy-agent/opa/v1/rego"

	"agentbox/pkg/bundle"
)

type GroupPolicy struct {
	Models       []string      `json:"models"`
	Egress       []string      `json:"egress"`
	Images       []string      `json:"images"`
	TierCap      int           `json:"tier_cap"`
	Limits       bundle.Limits `json:"limits"`
	RateLimitRPM int           `json:"rate_limit_rpm"`
	Mounts       []string      `json:"mounts"`
}

type EgressCatalog struct {
	Auto    []string `json:"auto"`
	Flagged []string `json:"flagged"`
	Never   []string `json:"never"`
}

type Catalog struct {
	Images       map[string]bundle.ImageRef `json:"images"`
	Egress       EgressCatalog              `json:"egress"`
	ProjectRoots []string                   `json:"project_roots"`
	Models       []string                   `json:"models"`
}

func DefaultGroup() GroupPolicy {
	return GroupPolicy{
		Models:       []string{"deepseek-v3", "glm-4"},
		Egress:       []string{"git.agentbox.corp"},
		Images:       []string{"base", "codex", "opencode"},
		TierCap:      3,
		Limits:       bundle.Limits{CPUs: 2, MemoryMB: 2048, PIDs: 512},
		RateLimitRPM: 120,
	}
}

func DefaultEgressCatalog() EgressCatalog {
	return EgressCatalog{
		Auto:    []string{"pypi.agentbox.corp", "npm.agentbox.corp", "proxy.golang.agentbox.corp", "docs.agentbox.corp"},
		Flagged: []string{"*.lab.agentbox.corp", "artifacts.agentbox.corp"},
		Never:   []string{"api.openai.com", "api.anthropic.com", "*.openai.com", "generativelanguage.googleapis.com"},
	}
}

type Device struct {
	ID             string
	User           string
	AssuranceLevel string
}

type User struct {
	Name  string
	Group string
	Tier  int
}

type Grant struct {
	ID         int64
	Permission string
	Class      string
	ExpiresAt  *time.Time
}

const BundleTTL = 24 * time.Hour

// Build assembles the bundle content (version and timestamps are set by the
// caller). Kill switches are not in bundles: they act at the gateway.
func Build(d Device, u User, g GroupPolicy, cat Catalog, grants []Grant, channel string) *bundle.Bundle {
	b := &bundle.Bundle{
		DeviceID: d.ID, User: u.Name, Group: u.Group, Tier: u.Tier,
		AssuranceLevel: d.AssuranceLevel,
		Models:         append([]string{}, g.Models...),
		Egress:         append([]string{}, g.Egress...),
		Images:         map[string]bundle.ImageRef{},
		Mounts:         append([]string{}, g.Mounts...),
		Limits:         g.Limits,
		RateLimitRPM:   g.RateLimitRPM,
		ReleaseChannel: channel,
	}
	for _, name := range g.Images {
		if ref, ok := cat.Images[name]; ok {
			b.Images[name] = ref
		}
	}
	for _, gr := range grants {
		kind, val, _ := strings.Cut(gr.Permission, ":")
		switch kind {
		case "egress":
			b.Egress = append(b.Egress, val)
		case "image":
			if ref, ok := cat.Images[val]; ok {
				b.Images[val] = ref
			}
		case "tier":
			if n, err := strconv.Atoi(val); err == nil && n > b.Tier && n <= g.TierCap {
				b.Tier = n
			}
		case "port":
			if n, err := strconv.Atoi(val); err == nil {
				b.Ports = append(b.Ports, n)
			}
		case "mount":
			b.Mounts = append(b.Mounts, val)
		case "model":
			b.Models = append(b.Models, val)
		}
		b.Grants = append(b.Grants, bundle.Grant{ID: strconv.FormatInt(gr.ID, 10), Permission: gr.Permission, Class: gr.Class, ExpiresAt: gr.ExpiresAt})
	}
	b.Egress = dedupe(b.Egress)
	b.Models = dedupe(b.Models)
	b.Mounts = dedupe(b.Mounts)
	return b
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// ContentHash identifies bundle content independent of version and times.
func ContentHash(b *bundle.Bundle) string {
	c := *b
	c.Version, c.IssuedAt, c.ExpiresAt = 0, time.Time{}, time.Time{}
	j, _ := json.Marshal(c)
	h := sha256.Sum256(j)
	return hex.EncodeToString(h[:])
}

//go:embed permissions.rego
var regoSrc string

type Classifier struct{ q rego.PreparedEvalQuery }

func NewClassifier(ctx context.Context) (*Classifier, error) {
	q, err := rego.New(
		rego.Query("data.agentbox.permissions.class"),
		rego.Module("permissions.rego", regoSrc),
	).PrepareForEval(ctx)
	if err != nil {
		return nil, err
	}
	return &Classifier{q: q}, nil
}

// ParsePermission validates "kind:value".
func ParsePermission(p string) (kind, value string, err error) {
	kind, value, ok := strings.Cut(strings.TrimSpace(p), ":")
	if !ok || value == "" {
		return "", "", fmt.Errorf("permission must look like kind:value, e.g. egress:pypi.agentbox.corp")
	}
	switch kind {
	case "egress", "image", "tier", "port", "mount", "model":
	default:
		return "", "", fmt.Errorf("unknown permission kind %q (egress, image, tier, port, mount, model)", kind)
	}
	if kind == "tier" || kind == "port" {
		if _, err := strconv.Atoi(value); err != nil {
			return "", "", fmt.Errorf("%s needs a number", kind)
		}
	}
	return kind, value, nil
}

func (c *Classifier) Classify(ctx context.Context, perm string, u User, g GroupPolicy, cat Catalog, autoRules []string) (string, error) {
	kind, value, err := ParsePermission(perm)
	if err != nil {
		return "", err
	}
	images := map[string]any{}
	for k := range cat.Images {
		images[k] = true
	}
	input := map[string]any{
		"permission": perm, "kind": kind, "value": value,
		"user":  map[string]any{"name": u.Name, "group": u.Group, "tier": u.Tier},
		"group": map[string]any{"images": g.Images, "tier_cap": g.TierCap},
		"catalog": map[string]any{
			"images": images,
			"egress": map[string]any{"auto": nz(cat.Egress.Auto), "flagged": nz(cat.Egress.Flagged), "never": nz(cat.Egress.Never)},
			"project_roots": nz(cat.ProjectRoots),
		},
		"auto_rules": nz(autoRules),
	}
	rs, err := c.q.Eval(ctx, rego.EvalInput(input))
	if err != nil {
		return "", err
	}
	if len(rs) == 0 || len(rs[0].Expressions) == 0 {
		return "approval", nil
	}
	s, _ := rs[0].Expressions[0].Value.(string)
	return s, nil
}

func nz(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
