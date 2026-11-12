package main

import (
	"testing"
	"time"

	"agentbox/pkg/bundle"
)

func TestNonModelPermissionsKeepApplyingForSeventyTwoHoursAfterExpiry(t *testing.T) {
	m := &Manager{}
	if b, why := m.bundleUsable(); b != nil || why == "" {
		t.Fatalf("no bundle: got %v %q, want a refusal", b, why)
	}
	cases := []struct {
		name        string
		expiredFor  time.Duration
		modelOK     bool
		nonModelOK  bool
	}{
		{"valid", -time.Hour, true, true},
		{"just expired", time.Minute, false, true},
		{"expired a day ago", 24 * time.Hour, false, true},
		{"71 hours", 71 * time.Hour, false, true},
		{"73 hours", 73 * time.Hour, false, false},
	}
	for _, c := range cases {
		m.bundle = &bundle.Bundle{ExpiresAt: time.Now().Add(-c.expiredFor), Egress: []string{"pypi.agentbox.corp"}}
		if _, why := m.bundleValid(); (why == "") != c.modelOK {
			t.Errorf("%s: model access allowed=%v, want %v (%s)", c.name, why == "", c.modelOK, why)
		}
		if _, why := m.bundleUsable(); (why == "") != c.nonModelOK {
			t.Errorf("%s: egress and images allowed=%v, want %v (%s)", c.name, why == "", c.nonModelOK, why)
		}
	}
}
