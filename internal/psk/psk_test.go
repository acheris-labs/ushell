package psk

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base32"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// RFC 8032, section 7.1, test 1.
const (
	rfcSeed   = "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	rfcPublic = "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a"
)

func rfcSeedText(t *testing.T) string {
	t.Helper()
	raw, _ := hex.DecodeString(rfcSeed)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))
}

func TestNewSeedIs52Base32Chars(t *testing.T) {
	a, err := NewSeed()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 52 || strings.Trim(a, "abcdefghijklmnopqrstuvwxyz234567") != "" {
		t.Fatalf("want 52 lowercase base32 chars, got %q", a)
	}
	b, _ := NewSeed()
	if a == b {
		t.Fatal("two new seeds should differ")
	}
}

func TestParseSeedIsForgiving(t *testing.T) {
	s := rfcSeedText(t)
	want, _ := ParseSeed(s)
	grouped := strings.ToUpper(s[:13] + "-" + s[13:26] + " " + s[26:39] + "\n" + s[39:])
	got, err := ParseSeed(grouped)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("case, dashes and whitespace should be ignored: %v", err)
	}
}

func TestParseSeedRejectsBadInput(t *testing.T) {
	for name, in := range map[string]string{
		"empty":     "",
		"too short": rfcSeedText(t)[:40],
		"too long":  rfcSeedText(t) + "aaaaaaaa",
		"not b32":   strings.Repeat("1", 52),
	} {
		if _, err := ParseSeed(in); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestFromSeedMatchesRFC8032(t *testing.T) {
	seed, err := ParseSeed(rfcSeedText(t))
	if err != nil {
		t.Fatal(err)
	}
	pub := FromSeed(seed).Public().(ed25519.PublicKey)
	if hex.EncodeToString(pub) != rfcPublic {
		t.Fatalf("public key %x, want %s", pub, rfcPublic)
	}
}

func TestWriteCreatesThenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "psk")
	seed, _ := ParseSeed(rfcSeedText(t))
	key := FromSeed(seed)

	created, err := Write(path, key)
	if err != nil || !created {
		t.Fatalf("first write: created=%v err=%v", created, err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode %v, want 0600", info.Mode().Perm())
	}
	pem, _ := os.ReadFile(path)
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		t.Fatalf("private key file does not parse: %v", err)
	}
	pubLine, _ := os.ReadFile(path + ".pub")
	pub, _, _, _, err := ssh.ParseAuthorizedKey(pubLine)
	if err != nil || !bytes.Equal(pub.Marshal(), signer.PublicKey().Marshal()) {
		t.Fatalf(".pub does not match the private key: %v", err)
	}

	created, err = Write(path, key)
	if err != nil || created {
		t.Fatalf("same key again should be a no-op: created=%v err=%v", created, err)
	}
}

func TestWriteRefusesADifferentKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "psk")
	a, _ := NewSeed()
	b, _ := NewSeed()
	sa, _ := ParseSeed(a)
	sb, _ := ParseSeed(b)
	if _, err := Write(path, FromSeed(sa)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, err := Write(path, FromSeed(sb)); err == nil {
		t.Fatal("writing a different key over an existing one must fail")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("existing key was modified")
	}
}

func TestWriteRestoresMissingPub(t *testing.T) {
	path := filepath.Join(t.TempDir(), "psk")
	s, _ := ParseSeed(rfcSeedText(t))
	if _, err := Write(path, FromSeed(s)); err != nil {
		t.Fatal(err)
	}
	os.Remove(path + ".pub")
	if _, err := Write(path, FromSeed(s)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".pub"); err != nil {
		t.Fatal(".pub should have been recreated")
	}
}

func TestHostKeysAreStableAndDistinct(t *testing.T) {
	seed, _ := ParseSeed(rfcSeedText(t))
	client := FromSeed(seed).Public().(ed25519.PublicKey)
	sshd := HostKey(seed, "sshd").Public().(ed25519.PublicKey)
	uptermd := HostKey(seed, "uptermd").Public().(ed25519.PublicKey)

	if !bytes.Equal(sshd, HostKey(seed, "sshd").Public().(ed25519.PublicKey)) {
		t.Fatal("host key must be the same every time for the same seed")
	}
	if bytes.Equal(sshd, client) || bytes.Equal(uptermd, client) || bytes.Equal(sshd, uptermd) {
		t.Fatal("host keys must differ from the login key and from each other")
	}

	// Pinned: changing the derivation changes every rendezvous's host keys,
	// which breaks known_hosts on every client. Do it deliberately or not at all.
	pinned := map[string]string{
		"sshd":    "SHA256:+3YzdtCVyytWT7vvnuAAsT4psKU2qtVIDhJGCeKHpwA",
		"uptermd": "SHA256:drVGh5FuK3C9j5zDjfJgFRoPma4ULPEdPbzC41hY7Pc",
	}
	for purpose, want := range pinned {
		pub, _ := ssh.NewPublicKey(HostKey(seed, purpose).Public())
		if got := ssh.FingerprintSHA256(pub); got != want {
			t.Errorf("%s host key fingerprint %s, pinned %s", purpose, got, want)
		}
	}
}
