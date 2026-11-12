package main

import "testing"

func TestWithoutHostReplacesOnlyThatEntry(t *testing.T) {
	cfg := "Host agentbox-a\n  ProxyCommand old a\n\nHost agentbox-ab\n  ProxyCommand keep ab\n\nHost agentbox-b\n  ProxyCommand keep b\n\n"
	got := withoutHost(cfg, "agentbox-a")
	want := "Host agentbox-ab\n  ProxyCommand keep ab\n\nHost agentbox-b\n  ProxyCommand keep b\n\n"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if withoutHost("", "agentbox-a") != "" {
		t.Fatal("empty config should stay empty")
	}
}

func TestSSHQuote(t *testing.T) {
	if got := sshQuote(`C:\Program Files\Agentbox\bin\agentbox.exe`); got != `"C:/Program Files/Agentbox/bin/agentbox.exe"` && got != `"C:\Program Files\Agentbox\bin\agentbox.exe"` {
		t.Fatalf("got %s", got)
	}
}
