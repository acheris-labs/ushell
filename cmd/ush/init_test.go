package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestEnsureKeyGeneratesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "psk")
	created, err := ensureKey(path)
	if err != nil || !created {
		t.Fatalf("first call should create: created=%v err=%v", created, err)
	}
	priv, _ := os.ReadFile(path)
	if _, err := ssh.ParsePrivateKey(priv); err != nil {
		t.Fatalf("private key unreadable: %v", err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode %v, want 0600", info.Mode().Perm())
	}

	created, err = ensureKey(path)
	if err != nil || created {
		t.Fatalf("second call must not create: created=%v err=%v", created, err)
	}
	again, _ := os.ReadFile(path)
	if !bytes.Equal(priv, again) {
		t.Fatal("existing key was overwritten")
	}
}

func TestEnsureKeyDerivesMissingPublicKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "psk")
	if _, err := ensureKey(path); err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(path + ".pub")
	os.Remove(path + ".pub")
	if _, err := ensureKey(path); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path + ".pub")
	wantKey, _, _, _, _ := ssh.ParseAuthorizedKey(want)
	gotKey, _, _, _, err := ssh.ParseAuthorizedKey(got)
	if err != nil || !bytes.Equal(wantKey.Marshal(), gotKey.Marshal()) {
		t.Fatalf("derived public key does not match: %v", err)
	}
}

func TestGatherPromptsForWhatFlagsDidNotGive(t *testing.T) {
	var out bytes.Buffer
	// profile name, rendezvous, port, key: blank = default
	in := strings.NewReader("\nrv.example.net\n2201\n\n")
	a, err := gather(initArgs{}, in, &out)
	if err != nil {
		t.Fatal(err)
	}
	if a.profile != "default" || a.rendezvous != "rv.example.net" ||
		a.port != 2201 || a.key != "~/.ssh/ush_ed25519" {
		t.Fatalf("got %+v", a)
	}
	if !strings.Contains(out.String(), "Rendezvous host") {
		t.Fatalf("expected a rendezvous prompt, got %q", out.String())
	}
}

func TestGatherSkipsPromptsForFlags(t *testing.T) {
	var out bytes.Buffer
	a, err := gather(initArgs{profile: "work", rendezvous: "rv", yes: true}, strings.NewReader(""), &out)
	if err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("-y must not prompt, printed %q", out.String())
	}
	if a.profile != "work" || a.rendezvous != "rv" || a.port != 7874 {
		t.Fatalf("got %+v", a)
	}
}

func TestGatherRequiresRendezvous(t *testing.T) {
	_, err := gather(initArgs{yes: true}, strings.NewReader(""), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "rendezvous") {
		t.Fatalf("want an error about the rendezvous, got %v", err)
	}
}

func TestGatherRejectsBadPort(t *testing.T) {
	_, err := gather(initArgs{}, strings.NewReader("\nrv\n99999\n"), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "port") {
		t.Fatalf("want a port error, got %v", err)
	}
}
