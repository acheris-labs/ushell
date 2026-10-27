package config

import (
	"os"
	"strings"
	"testing"
)

const machines = `
machine = "Alex's MacBook"
default = "home"

[profiles.home]
rendezvous = "rv.home"

[profiles.work]
rendezvous = "rv.work"
machine = "alex-work"

[profiles.blank]
rendezvous = "rv.blank"
machine = ""
`

func TestMachineName(t *testing.T) {
	for profile, want := range map[string]string{
		"home":  "Alex's MacBook", // top-level applies
		"work":  "alex-work",      // profile overrides
		"blank": "Alex's MacBook", // empty means unset, so top-level applies
	} {
		if got := loadText(t, machines, profile).Machine; got != want {
			t.Errorf("%s: machine %q, want %q", profile, got, want)
		}
	}
}

func TestNoMachineIsTheShortHostname(t *testing.T) {
	cfg := loadText(t, "default = \"a\"\n[profiles.a]\nrendezvous = \"x\"\n", "")
	h, _ := os.Hostname()
	if want, _, _ := strings.Cut(h, "."); cfg.Machine != want {
		t.Fatalf("no machine anywhere: got %q, want the short hostname %q", cfg.Machine, want)
	}
}

func TestMuxSetting(t *testing.T) {
	const file = `
mux = "herdr"
default = "home"

[profiles.home]
rendezvous = "rv.home"

[profiles.work]
rendezvous = "rv.work"
mux = "tmux"
`
	if got := loadText(t, file, "home").Mux; got != "herdr" {
		t.Errorf("top-level mux: got %q", got)
	}
	if got := loadText(t, file, "work").Mux; got != "tmux" {
		t.Errorf("profile should override: got %q", got)
	}
	if got := loadText(t, "default = \"a\"\n[profiles.a]\nrendezvous = \"x\"\n", "").Mux; got != "" {
		t.Errorf("unset mux means auto-detect: got %q", got)
	}
}
