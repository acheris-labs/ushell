package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var home = Profile{Rendezvous: "rv.example.net", Port: 7874, Key: "~/.ssh/ush_ed25519"}

func loadText(t *testing.T, text, name string) Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ushrc")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, "/home/me", name)
	if err != nil {
		t.Fatalf("result does not load: %v\n---\n%s", err, text)
	}
	return cfg
}

func TestAddProfileToEmptyFile(t *testing.T) {
	out, err := AddProfile("", "home", home, false)
	if err != nil {
		t.Fatal(err)
	}
	cfg := loadText(t, out, "")
	if cfg.Profile != "home" || cfg.Rendezvous != "rv.example.net" {
		t.Fatalf("new file should default to the new profile, got %+v\n%s", cfg, out)
	}
}

const existing = `# my comment survives
default = "local"

[profiles.local]
rendezvous = "127.0.0.1"
port = 7874
`

func TestAddProfileKeepsExistingDefaultAndComments(t *testing.T) {
	out, err := AddProfile(existing, "home", home, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "# my comment survives") {
		t.Fatalf("comment lost:\n%s", out)
	}
	if loadText(t, out, "").Profile != "local" {
		t.Fatalf("default should stay local:\n%s", out)
	}
	if loadText(t, out, "home").Rendezvous != "rv.example.net" {
		t.Fatalf("new profile not loadable:\n%s", out)
	}
}

func TestAddProfileCanTakeOverDefault(t *testing.T) {
	out, err := AddProfile(existing, "home", home, true)
	if err != nil {
		t.Fatal(err)
	}
	if loadText(t, out, "").Profile != "home" {
		t.Fatalf("default should now be home:\n%s", out)
	}
	if strings.Count(out, "default =") != 1 {
		t.Fatalf("want exactly one default line:\n%s", out)
	}
}

func TestAddProfileInsertsMissingDefaultAtTopLevel(t *testing.T) {
	// A top-level key after a table header would land inside that table, so
	// the default has to go above the first header.
	noDefault := "[profiles.local]\nrendezvous = \"127.0.0.1\"\n"
	out, err := AddProfile(noDefault, "home", home, false)
	if err != nil {
		t.Fatal(err)
	}
	if loadText(t, out, "").Profile != "home" {
		t.Fatalf("file had no default, so the new profile should become it:\n%s", out)
	}
}

func TestAddProfileRefusesDuplicates(t *testing.T) {
	_, err := AddProfile(existing, "local", home, false)
	if err == nil || !strings.Contains(err.Error(), "local") {
		t.Fatalf("want an error naming the existing profile, got %v", err)
	}
}

func TestAddProfileRejectsBadNames(t *testing.T) {
	for _, name := range []string{"", "has space", "dot.ted", "quo\"te"} {
		if _, err := AddProfile("", name, home, false); err == nil {
			t.Errorf("name %q should be rejected", name)
		}
	}
}

func TestAddProfileEscapesValues(t *testing.T) {
	weird := home
	weird.Key = `C:\keys\"psk"`
	out, err := AddProfile("", "home", weird, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := loadText(t, out, "").Key; got != weird.Key {
		t.Fatalf("value mangled: got %q want %q\n%s", got, weird.Key, out)
	}
}
