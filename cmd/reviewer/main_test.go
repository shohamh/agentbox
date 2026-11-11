package main

import "testing"

func TestParseVerdict(t *testing.T) {
	cases := map[string]string{
		`{"verdict":"consistent","reason":"fine"}`:       "consistent",
		`{"verdict":"unusual","reason":"odd"}`:           "unusual",
		`{"verdict":"approve","reason":"x"}`:             "escalate",
		`I think it is fine`:                             "escalate",
		`{"verdict":"consistent","reason":"x","tool":1}`: "escalate",
	}
	for in, want := range cases {
		if got, _ := parseVerdict(in); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
}
