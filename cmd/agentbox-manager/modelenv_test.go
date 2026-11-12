package main

import (
	"strings"
	"testing"

	"agentbox/pkg/bundle"
)

func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	return out
}

func TestModelEnvNamesOnlyAllowedModels(t *testing.T) {
	m := &Manager{bundle: &bundle.Bundle{Models: []string{"claude-sonnet", "claude-haiku", "gpt-5"}}}
	e := envMap(m.modelEnv(&Sandbox{}))
	if e["AGENTBOX_MODEL"] != "claude-sonnet" || e["ANTHROPIC_MODEL"] != "claude-sonnet" || e["CLAUDE_CODE_SUBAGENT_MODEL"] != "claude-sonnet" {
		t.Errorf("default model wrong: %v", e)
	}
	if e["ANTHROPIC_DEFAULT_HAIKU_MODEL"] != "claude-haiku" || e["AGENTBOX_SMALL_MODEL"] != "claude-haiku" {
		t.Errorf("small model wrong: %v", e)
	}
	if e["AGENTBOX_MODELS"] != "claude-sonnet,claude-haiku,gpt-5" {
		t.Errorf("model list wrong: %q", e["AGENTBOX_MODELS"])
	}
	// every model name in the environment is in the policy: nothing a gateway would refuse
	allowed := map[string]bool{"claude-sonnet": true, "claude-haiku": true, "gpt-5": true}
	for k, v := range e {
		if k != "AGENTBOX_MODELS" && !allowed[v] {
			t.Errorf("%s=%s is not an allowed model", k, v)
		}
	}
}

func TestSmallModelMatchesWholeWords(t *testing.T) {
	cases := []struct {
		models []string
		want   string
	}{
		{[]string{"deepseek-v3", "glm-4"}, "deepseek-v3"},          // nothing small: the default
		{[]string{"deepseek-v3", "small"}, "small"},                 // the test bed
		{[]string{"gemini-2.5-pro", "gemini-2.5-flash"}, "gemini-2.5-flash"},
		{[]string{"gemini-2.5-pro"}, "gemini-2.5-pro"},              // "gemini" contains "mini" but is not small
		{[]string{"gpt-5", "gpt-5-mini"}, "gpt-5-mini"},
		{[]string{"claude-sonnet", "us.anthropic.claude-haiku-v1"}, "us.anthropic.claude-haiku-v1"},
	}
	for _, c := range cases {
		if got := smallModel(c.models); got != c.want {
			t.Errorf("smallModel(%v) = %s, want %s", c.models, got, c.want)
		}
	}
}

func TestModelEnvWithoutPolicy(t *testing.T) {
	if env := (&Manager{}).modelEnv(&Sandbox{}); env != nil {
		t.Errorf("no bundle must give no model environment, got %v", env)
	}
}

func TestModelEnvSurvivesARestartWithoutABundle(t *testing.T) {
	m := &Manager{bundle: &bundle.Bundle{Models: []string{"claude-sonnet", "claude-haiku"}}}
	s := &Sandbox{}
	first := envMap(m.modelEnv(s))
	if first["AGENTBOX_MODEL"] != "claude-sonnet" || len(s.ModelEnv) == 0 {
		t.Fatalf("model env not computed or not remembered: %v", first)
	}
	// the appliance restarts the sandbox while no policy bundle is loaded
	m.bundle = nil
	again := envMap(m.modelEnv(s))
	if again["AGENTBOX_MODEL"] != "claude-sonnet" || again["AGENTBOX_MODELS"] != "claude-sonnet,claude-haiku" {
		t.Fatalf("the sandbox lost its model names without a bundle: %v", again)
	}
	// a new policy replaces what was remembered
	m.bundle = &bundle.Bundle{Models: []string{"gpt-5"}}
	if e := envMap(m.modelEnv(s)); e["AGENTBOX_MODEL"] != "gpt-5" {
		t.Fatalf("a new policy was ignored: %v", e)
	}
}
