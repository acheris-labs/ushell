package main

import (
	"os"
	"strings"
	"testing"

	"ush/internal/keep"
	"ush/internal/register"
)

func TestSessionSetRelabelsTheShareThisShellIsIn(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ushs") // short: a unix socket path is ~100 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("USH_KEEP_DIR", dir)
	kd, err := keep.Dir()
	if err != nil {
		t.Fatal(err)
	}
	label, stop := controlled("api", "", func(string, ...any) {})
	defer stop()
	if _, err := os.Stat(register.ControlPath(kd, "api")); err != nil {
		t.Fatalf("no control socket: %v", err)
	}
	env := func(k string) string {
		if k == "USH_SESSION" {
			return "api"
		}
		return ""
	}

	var out strings.Builder
	if code := runSession([]string{"set", "-d", "pr monitor", "-n", "api-v2"}, env, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if n, d := label.Get(); n != "api-v2" || d != "pr monitor" {
		t.Fatalf("label is %q, %q", n, d)
	}
	if got := out.String(); got != "api will be listed as api-v2: pr monitor\n" {
		t.Fatalf("said %q", got)
	}
	// Named explicitly, by the name it started with; -d '' clears.
	out.Reset()
	if code := runSession([]string{"set", "-d", "", "api"}, func(string) string { return "" }, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if _, d := label.Get(); d != "" {
		t.Fatalf("description is %q, want it cleared", d)
	}
}

func TestSessionSetRefusesWhatItCannotDo(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ushs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("USH_KEEP_DIR", dir)
	none := func(string) string { return "" }
	for _, c := range []struct {
		args []string
		code int
	}{
		{nil, 2},
		{[]string{"get"}, 2},
		{[]string{"set", "api"}, 2},               // nothing to set
		{[]string{"set", "-d", "x"}, 2},           // no session named, none in $USH_SESSION
		{[]string{"set", "-d", "x", "a", "b"}, 2}, // two sessions
		{[]string{"set", "-d", "x", "../up"}, 2},  // not a name, so not a path either
		{[]string{"set", "-d", "x", "api"}, 1},    // nothing listening
	} {
		if code := runSession(c.args, none, &strings.Builder{}); code != c.code {
			t.Errorf("runSession(%q) = %d, want %d", c.args, code, c.code)
		}
	}
}
