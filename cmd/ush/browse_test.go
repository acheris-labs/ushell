package main

import (
	"slices"
	"strings"
	"testing"

	"ush/internal/config"
	"ush/internal/detach"
)

func TestBrowseArgsCarryConfig(t *testing.T) {
	cfg := config.Config{
		Rendezvous: "rv.example.net", Port: "7874", User: "ush",
		Key: "/keys/psk", KnownHosts: "/home/me/.ssh/known_hosts",
	}
	args := browseArgs(cfg)
	for _, want := range [][]string{
		{"-i", "/keys/psk"},
		{"-p", "7874"},
		{"-o", "IdentitiesOnly=yes"},
		{"-o", "StrictHostKeyChecking=accept-new"},
		{"-o", "UserKnownHostsFile=/home/me/.ssh/known_hosts"},
	} {
		if !containsPair(args, want) {
			t.Errorf("missing %v in %v", want, args)
		}
	}
	if args[0] != "ssh" || args[1] != "-t" {
		t.Errorf("want ssh -t ..., got %v", args)
	}
	if args[len(args)-1] != "ush@rv.example.net" {
		t.Errorf("destination must be last, got %v", args)
	}
}

func TestBrowseArgsSkipHostKeyCheck(t *testing.T) {
	cfg := config.Config{Rendezvous: "127.0.0.1", Port: "7874", User: "ush",
		Key: "/k", KnownHosts: "/kh", SkipHostKeyCheck: true}
	args := browseArgs(cfg)
	if !containsPair(args, []string{"-o", "StrictHostKeyChecking=no"}) ||
		!containsPair(args, []string{"-o", "UserKnownHostsFile=/dev/null"}) {
		t.Errorf("skip mode should disable host key checking: %v", args)
	}
	if slices.Contains(args, "UserKnownHostsFile=/kh") {
		t.Errorf("skip mode must not touch the real known_hosts: %v", args)
	}
}

func containsPair(args, pair []string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == pair[0] && args[i+1] == pair[1] {
			return true
		}
	}
	return false
}

func TestBrowseCleansUpAfterSSH(t *testing.T) {
	for _, tc := range []struct {
		code       int
		isTerminal bool
		want       string
	}{
		{0, true, detach.ResetModes},
		{255, true, detach.ResetModes + "\x1b[?1049l"}, // the menu's own cleanup never came
		{255, false, ""},
	} {
		var out strings.Builder
		resetAfterBrowse(&out, tc.isTerminal, tc.code)
		if out.String() != tc.want {
			t.Errorf("exit %d, terminal %v: wrote %q, want %q", tc.code, tc.isTerminal, out.String(), tc.want)
		}
	}
}
