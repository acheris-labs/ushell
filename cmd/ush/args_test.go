package main

import (
	"slices"
	"strings"
	"testing"

	"ush/internal/config"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestConnectIsTheDefault(t *testing.T) {
	for _, args := range [][]string{{}, {"--profile", "work"}} {
		o, err := parseArgs(args, env(nil))
		if err != nil || o.share {
			t.Errorf("%v: want connect, got share=%v err=%v", args, o.share, err)
		}
	}
}

// Sharing has its own spelling now, so a sharing option on the connect path is
// a typo worth naming rather than a mode to infer.
func TestSharingOptionsAreNotConnectOptions(t *testing.T) {
	for _, args := range [][]string{
		{"-n", "api"}, {"-d", "x"}, {"--read-only"}, {"-k"}, {"-c"}, {"claude"},
	} {
		if _, err := parseArgs(args, env(nil)); err == nil {
			t.Errorf("%v should be refused on the connect path", args)
		}
	}
	// And the refusal points at the right spelling.
	_, err := parseArgs([]string{"claude"}, env(nil))
	if err == nil || !strings.Contains(err.Error(), "ush share") {
		t.Errorf("error %v should point at ush share", err)
	}
}

func TestShareParsesCommand(t *testing.T) {
	o, err := parseArgs([]string{"share", "-n", "api", "-d", "desc", "--read-only", "--", "claude", "--resume"}, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !o.share || o.name != "api" || o.desc != "desc" || !o.readOnly ||
		!slices.Equal(o.command, []string{"claude", "--resume"}) {
		t.Fatalf("got %+v", o)
	}
}

func TestEverySpellingOfShareShares(t *testing.T) {
	for _, s := range []string{"share", "serve", "-s"} {
		o, err := parseArgs([]string{s, "-k", "-n", "api", "--", "claude"}, env(nil))
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if !o.share || !o.keep || o.name != "api" || !slices.Equal(o.command, []string{"claude"}) {
			t.Fatalf("%s: got %+v", s, o)
		}
	}
}

func TestShareSpelledLateSaysToPutItFirst(t *testing.T) {
	for _, args := range [][]string{{"-n", "api", "-s"}, {"--profile", "work", "share"}, {"claude", "serve"}} {
		_, err := parseArgs(args, env(nil))
		if err == nil || !strings.Contains(err.Error(), "goes first") {
			t.Errorf("%v: error %v should say where the share spelling goes", args, err)
		}
	}
}

func TestShareDefaultsToShell(t *testing.T) {
	o, err := parseArgs([]string{"share"}, env(map[string]string{"SHELL": "/bin/zsh"}))
	if err != nil {
		t.Fatal(err)
	}
	if !o.share || !slices.Equal(o.command, []string{"/bin/zsh"}) {
		t.Fatalf("got %+v", o)
	}
}

func TestProfilePrecedence(t *testing.T) {
	for _, c := range []struct {
		args []string
		vars map[string]string
		want string
	}{
		{[]string{"--profile", "work"}, nil, "work"},
		{[]string{"share", "--profile", "work"}, nil, "work"},
		{nil, map[string]string{"USH_PROFILE": "home"}, "home"},
		{[]string{"share"}, map[string]string{"USH_PROFILE": "home"}, "home"},
		{[]string{"--profile", "work"}, map[string]string{"USH_PROFILE": "home"}, "work"},
		{nil, nil, ""},
	} {
		o, err := parseArgs(c.args, env(c.vars))
		if err != nil || o.profile != c.want {
			t.Errorf("%v with %v: profile %q, want %q (err %v)", c.args, c.vars, o.profile, c.want, err)
		}
	}
}

func TestKeepRules(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"share", "--mux", "tmux"}, "--mux only applies"},
		// One flag keeps a session now; the old ones are just unknown.
		{[]string{"share", "-t", "-n", "api"}, "not defined"},
		{[]string{"share", "-K", "-n", "api", "--", "top"}, "not defined"},
	} {
		_, err := parseArgs(c.args, env(nil))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: want an error mentioning %q, got %v", c.args, c.want, err)
		}
	}
}

func TestKeepShellDefaultsToShell(t *testing.T) {
	o, err := parseArgs([]string{"share", "--keep", "-n", "api"}, env(map[string]string{"SHELL": "/bin/zsh"}))
	if err != nil {
		t.Fatal(err)
	}
	if !o.keep || !slices.Equal(o.command, []string{"/bin/zsh"}) {
		t.Fatalf("got %+v", o)
	}
}

// Sharing or connecting from inside a shared terminal would nest a session
// inside itself, so it is refused; everything else still runs.
func TestNestedSessionIsDetected(t *testing.T) {
	inside := func(k string) string {
		if k == "USH_SESSION" {
			return "api"
		}
		return ""
	}
	if got := nestedIn(inside); got != "api" {
		t.Errorf("inside a session: %q, want %q", got, "api")
	}
	if got := nestedIn(func(string) string { return "" }); got != "" {
		t.Errorf("outside a session: %q, want empty", got)
	}
}

// -k keeps whatever it is given: the shell when nothing is, the command when
// there is one. The backend is --mux, left for keepSharing to resolve against
// the config.
func TestKeepTakesAShellOrACommand(t *testing.T) {
	o, err := parseArgs([]string{"share", "-k", "-n", "api"}, env(map[string]string{"SHELL": "/bin/zsh"}))
	if err != nil {
		t.Fatal(err)
	}
	if !o.keep || o.keepCommand || !slices.Equal(o.command, []string{"/bin/zsh"}) {
		t.Errorf("-k alone: %+v, want a kept shell", o)
	}
	o, err = parseArgs([]string{"share", "-k", "--mux", "tmux", "-n", "api", "--", "top"}, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !o.keep || !o.keepCommand || o.mux != "tmux" || !slices.Equal(o.command, []string{"top"}) {
		t.Errorf("-k --mux tmux -- top: %+v, want top kept in tmux", o)
	}
}

func TestTheDefaultNameIsTheMachineName(t *testing.T) {
	for machine, want := range map[string]string{
		"umber":         "umber",
		"Alex's MacBook": "Alex-s-MacBook",
		"  host.lan  ":    "host-lan",
		"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		"???":        "unknown",
		"_build_box": "build_box", // a name starts with a letter or digit
	} {
		if got := defaultName(config.Config{Machine: machine}); got != want {
			t.Errorf("machine %q: default name %q, want %q", machine, got, want)
		}
	}
}

// A name is never read as a flag by ush attach, herdr or tmux.
func TestNamesStartWithALetterOrDigit(t *testing.T) {
	for name, want := range map[string]bool{
		"api": true, "9lives": true, "a.b_c-d": true, strings.Repeat("a", 32): true,
		"-read-only": false, ".hidden": false, "_x": false, "": false, strings.Repeat("a", 33): false,
	} {
		if got := validName.MatchString(name); got != want {
			t.Errorf("%q: valid=%v, want %v", name, got, want)
		}
	}
}
