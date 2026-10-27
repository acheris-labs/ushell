package psk

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

type vector struct {
	Seed           string `json:"seed"`
	LoginPublicKey string `json:"login_public_key"`
	HostPublicKey  string `json:"host_public_key"`
	Payload        string `json:"payload"`
}

func loadVector(t *testing.T) vector {
	t.Helper()
	b, err := os.ReadFile("../../test/vectors/pair.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vector
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func authorizedLine(t *testing.T, k ed25519.PrivateKey) string {
	t.Helper()
	pub, err := ssh.NewPublicKey(k.Public())
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
}

// The iOS app derives the same keys from the same seed; this vector is what
// its tests check against, so the two sides cannot drift apart.
func TestVectorMatchesDerivation(t *testing.T) {
	v := loadVector(t)
	seed, err := ParseSeed(v.Seed)
	if err != nil {
		t.Fatal(err)
	}
	if got := authorizedLine(t, FromSeed(seed)); got != v.LoginPublicKey {
		t.Errorf("login key %s, vector says %s", got, v.LoginPublicKey)
	}
	if got := authorizedLine(t, HostKey(seed, "sshd")); got != v.HostPublicKey {
		t.Errorf("host key %s, vector says %s", got, v.HostPublicKey)
	}
	if got := EncodeSeed(seed); got != v.Seed {
		t.Errorf("EncodeSeed gave %s, want %s", got, v.Seed)
	}
}

func TestSeedFromKeyFileRecoversTheSeed(t *testing.T) {
	v := loadVector(t)
	seed, _ := ParseSeed(v.Seed)
	path := filepath.Join(t.TempDir(), "key")
	if _, err := Write(path, FromSeed(seed)); err != nil {
		t.Fatal(err)
	}
	got, err := SeedFromKeyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if EncodeSeed(got) != v.Seed {
		t.Fatalf("recovered %s, want %s", EncodeSeed(got), v.Seed)
	}
}

func TestSeedFromKeyFileRefusesOtherKeyTypes(t *testing.T) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(k, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ecdsa")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SeedFromKeyFile(path); err == nil {
		t.Fatal("an ECDSA key was accepted")
	}
}
