package policy

import (
	"context"
	"testing"

	"agentbox/pkg/bundle"
)

func TestClassify(t *testing.T) {
	ctx := context.Background()
	c, err := NewClassifier(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cat := Catalog{
		Images:       map[string]bundle.ImageRef{"base": {}, "codex": {}, "gpu": {}},
		Egress:       DefaultEgressCatalog(),
		ProjectRoots: []string{"/home/*/src/*"},
	}
	g := DefaultGroup()
	u := User{Name: "alice", Group: "default", Tier: 1}
	cases := map[string]string{
		"egress:pypi.agentbox.corp":     "instant",
		"egress:x.lab.agentbox.corp":    "flagged",
		"egress:api.openai.com":         "never",
		"egress:example.org":            "approval",
		"image:codex":                   "instant",
		"image:gpu":                     "approval",
		"image:nope":                    "never",
		"tier:1":                        "instant",
		"tier:2":                        "approval",
		"tier:9":                        "never",
		"port:8080":                     "flagged",
		"port:4000":                     "never",
		"mount:/home/alice/src/project": "flagged",
		"mount:/etc":                    "never",
		"mount:/opt/data":               "approval",
	}
	for perm, want := range cases {
		got, err := c.Classify(ctx, perm, u, g, cat, nil)
		if err != nil {
			t.Fatalf("%s: %v", perm, err)
		}
		if got != want {
			t.Errorf("%s: got %s want %s", perm, got, want)
		}
	}
	got, _ := c.Classify(ctx, "egress:example.org", u, g, cat, []string{"egress:example.org"})
	if got != "instant" {
		t.Errorf("auto rule: got %s", got)
	}
}

func TestBuildGrants(t *testing.T) {
	cat := Catalog{Images: map[string]bundle.ImageRef{"base": {Ref: "r/base", Digest: "sha256:1"}, "gpu": {Ref: "r/gpu"}}}
	b := Build(Device{ID: "pc1"}, User{Name: "a", Tier: 1}, DefaultGroup(), cat,
		[]Grant{{ID: 1, Permission: "egress:pypi.agentbox.corp"}, {ID: 2, Permission: "image:gpu"}, {ID: 3, Permission: "tier:2"}}, "broad")
	if !bundle.EgressAllowed(b.Egress, "pypi.agentbox.corp") || b.Images["gpu"].Ref != "r/gpu" || b.Tier != 2 {
		t.Fatalf("grants not applied: %+v", b)
	}
	h1 := ContentHash(b)
	b.Version = 9
	if ContentHash(b) != h1 {
		t.Fatal("content hash must ignore version")
	}
}
