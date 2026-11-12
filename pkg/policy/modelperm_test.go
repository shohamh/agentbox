package policy

import (
	"context"
	"testing"
)

func TestClassifyModelPermissions(t *testing.T) {
	ctx := context.Background()
	c, err := NewClassifier(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cat := Catalog{Models: []string{"deepseek-v3", "glm-4", "small"}, Egress: DefaultEgressCatalog()}
	g := DefaultGroup() // models: deepseek-v3, glm-4
	u := User{Name: "alice", Group: "default", Tier: 1}
	cases := map[string]string{
		"model:deepseek-v3":  "instant",  // already in the group
		"model:small":        "approval", // served by the platform, not in the group: an approver decides
		"model:gpt-unknown":  "never",    // not served at all
		"tier:3":             "approval",
		"tier:4":             "never",
		"egress:example.org": "approval",
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
}

func TestBuildAppliesGrantedModelsAndTiersWithinTheCap(t *testing.T) {
	g := DefaultGroup()
	g.TierCap = 2
	u := User{Name: "alice", Group: "default", Tier: 1}
	grants := []Grant{{ID: 1, Permission: "model:small", Class: "approval"}, {ID: 2, Permission: "tier:2", Class: "approval"}, {ID: 3, Permission: "tier:3", Class: "approval"}}
	b := Build(Device{ID: "pc-1", User: "alice", AssuranceLevel: "C"}, u, g, Catalog{Models: []string{"small"}}, grants, "broad")
	found := false
	for _, m := range b.Models {
		found = found || m == "small"
	}
	if !found {
		t.Errorf("granted model missing from the bundle: %v", b.Models)
	}
	if b.Tier != 2 {
		t.Errorf("tier = %d, want 2: tier 2 is granted, tier 3 is above the group's cap", b.Tier)
	}
}
