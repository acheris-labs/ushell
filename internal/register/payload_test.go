package register

import (
	"encoding/base64"
	"strings"
	"testing"
)

func decode(t *testing.T, arg string) map[string]string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(arg)
	if err != nil {
		t.Fatalf("payload is not base64: %v", err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("line without '=': %q", line)
		}
		out[k] = v
	}
	return out
}

func TestCommandIsASingleArgument(t *testing.T) {
	cmd := Command(Info{Name: "api", Description: "has spaces in it"})
	parts := strings.Fields(cmd)
	if len(parts) != 2 || parts[0] != "share" {
		t.Fatalf("want exactly `share <blob>`, got %q", cmd)
	}
}

func TestEmptyFieldsSurvive(t *testing.T) {
	// The bug that motivated this format: an empty description vanished and
	// shifted every later field.
	got := decode(t, strings.Fields(Command(Info{Name: "api"}))[1])
	if v, ok := got["description"]; !ok || v != "" {
		t.Fatalf("empty description lost: %v", got)
	}
	if got["name"] != "api" || got["machine"] != "" {
		t.Fatalf("fields shifted: %v", got)
	}
}

func TestAllFieldsRoundTrip(t *testing.T) {
	in := Info{
		Name: "api", Description: "staging debug",
		Machine: "laptop", Command: "claude --resume", Cwd: "/Users/me/src/api",
		User: "me", ReadOnly: true,
	}
	got := decode(t, strings.Fields(Command(in))[1])
	want := map[string]string{
		"name": "api", "description": "staging debug",
		"machine": "laptop", "command": "claude --resume", "cwd": "/Users/me/src/api",
		"user": "me", "read_only": "true",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected fields: %v", got)
	}
}

func TestNewlinesCannotForgeFields(t *testing.T) {
	// A description containing "\nname=..." must not override the real one.
	in := Info{Name: "api", Description: "x\nname=forged"}
	got := decode(t, strings.Fields(Command(in))[1])
	if got["name"] != "api" {
		t.Fatalf("name forged via newline: %v", got)
	}
}

func TestNoSessionFieldIsSent(t *testing.T) {
	// The rendezvous assigns the id; a client-chosen one would let a sharer
	// collide with, or pose as, another session.
	got := decode(t, strings.Fields(Command(Info{Name: "api"}))[1])
	if _, ok := got["session"]; ok {
		t.Fatalf("payload carries a session field: %v", got)
	}
}
