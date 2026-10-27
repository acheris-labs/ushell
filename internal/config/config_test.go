package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ushrc")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const twoProfiles = `
default = "local"

[profiles.local]
rendezvous = "127.0.0.1"
port = 7874
key = "~/src/ut/.local/psk"
skip_host_key_check = true

[profiles.work]
rendezvous = "rendezvous.example.net"
`

func TestDefaultProfileFromFile(t *testing.T) {
	cfg, err := Load(write(t, twoProfiles), "/home/me", "")
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Profile:          "local",
		Machine:          shortHostname(),
		Rendezvous:       "127.0.0.1",
		Port:             "7874",
		User:             "ush",
		Key:              "/home/me/src/ut/.local/psk",
		KnownHosts:       "/home/me/.ssh/known_hosts",
		SkipHostKeyCheck: true,
	}
	if cfg != want {
		t.Fatalf("got  %+v\nwant %+v", cfg, want)
	}
}

func TestRequestedProfileWinsAndGetsDefaults(t *testing.T) {
	cfg, err := Load(write(t, twoProfiles), "/home/me", "work")
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Profile:    "work",
		Machine:    shortHostname(),
		Rendezvous: "rendezvous.example.net",
		Port:       "7874",
		User:       "ush",
		Key:        "/home/me/.ssh/ush_ed25519",
		KnownHosts: "/home/me/.ssh/known_hosts",
	}
	if cfg != want {
		t.Fatalf("got  %+v\nwant %+v", cfg, want)
	}
}

func TestUnknownProfileListsTheRealOnes(t *testing.T) {
	_, err := Load(write(t, twoProfiles), "/home/me", "wrok")
	if err == nil || !strings.Contains(err.Error(), "wrok") ||
		!strings.Contains(err.Error(), "local") || !strings.Contains(err.Error(), "work") {
		t.Fatalf("want an error naming the typo and the available profiles, got %v", err)
	}
}

func TestNoProfileSelected(t *testing.T) {
	_, err := Load(write(t, "[profiles.a]\nrendezvous = \"x\"\n"), "/home/me", "")
	if err == nil || !strings.Contains(err.Error(), "default") {
		t.Fatalf("want an error explaining how to pick a profile, got %v", err)
	}
}

func TestUnknownKeysAreRejected(t *testing.T) {
	// A typo must fail loudly rather than silently fall back to a default.
	for name, body := range map[string]string{
		"in a profile": "default = \"a\"\n[profiles.a]\nrendezvous = \"x\"\nssh_prot = 22\n",
		"top level":    "default = \"a\"\ndefualt = \"b\"\n[profiles.a]\nrendezvous = \"x\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(write(t, body), "/home/me", "")
			if err == nil || (!strings.Contains(err.Error(), "ssh_prot") && !strings.Contains(err.Error(), "defualt")) {
				t.Fatalf("want an error naming the unknown key, got %v", err)
			}
		})
	}
}

func TestRendezvousRequired(t *testing.T) {
	_, err := Load(write(t, "default = \"a\"\n[profiles.a]\nport = 22\n"), "/home/me", "")
	if err == nil || !strings.Contains(err.Error(), "rendezvous") {
		t.Fatalf("want an error naming rendezvous, got %v", err)
	}
}

func TestPortRange(t *testing.T) {
	_, err := Load(write(t, "default = \"a\"\n[profiles.a]\nrendezvous = \"x\"\nport = 70000\n"), "/home/me", "")
	if err == nil || !strings.Contains(err.Error(), "port 70000") {
		t.Fatalf("want a port range error, got %v", err)
	}
}

func TestMalformedTOML(t *testing.T) {
	_, err := Load(write(t, "USH_RENDEZVOUS=127.0.0.1\n"), "/home/me", "")
	if err == nil {
		t.Fatal("the old KEY=VALUE format must be rejected, not half-parsed")
	}
}

func TestAuthorizedKeysIsGone(t *testing.T) {
	body := "default = \"a\"\n[profiles.a]\nrendezvous = \"x\"\nauthorized_keys = \"~/x.pub\"\n"
	_, err := Load(write(t, body), "/home/me", "")
	if err == nil || !strings.Contains(err.Error(), "authorized_keys") || !strings.Contains(err.Error(), "remove it") {
		t.Fatalf("want an error explaining authorized_keys is gone, got %v", err)
	}
}

func TestOldPortKeysExplainTheRename(t *testing.T) {
	for _, key := range []string{"ssh_port", "uptermd_port"} {
		body := "default = \"a\"\n[profiles.a]\nrendezvous = \"x\"\n" + key + " = 7874\n"
		_, err := Load(write(t, body), "/home/me", "")
		if err == nil || !strings.Contains(err.Error(), "single port") {
			t.Errorf("%s: want an error explaining the rename to port, got %v", key, err)
		}
	}
}
