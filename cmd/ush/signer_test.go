package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestLoadSignerUsesOnlyTheFile(t *testing.T) {
	// The agent must never be consulted: on a machine with an agent,
	// offering its keys first would get the share rejected.
	t.Setenv("SSH_AUTH_SOCK", "/nonexistent/agent.sock")
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(priv, "")
	path := filepath.Join(t.TempDir(), "k")
	os.WriteFile(path, pem.EncodeToMemory(block), 0o600)

	s, err := loadSigner(path)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := ssh.NewPublicKey(priv.Public())
	if ssh.FingerprintSHA256(s.PublicKey()) != ssh.FingerprintSHA256(want) {
		t.Fatal("signer is not the key in the file")
	}
}

func TestLoadSignerErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := loadSigner(filepath.Join(dir, "missing")); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("want an error naming the missing file, got %v", err)
	}

	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("secret"))
	locked := filepath.Join(dir, "locked")
	os.WriteFile(locked, pem.EncodeToMemory(block), 0o600)
	if _, err := loadSigner(locked); err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("want a passphrase error, got %v", err)
	}
}
