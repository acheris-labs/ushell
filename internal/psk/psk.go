// Package psk derives the shared ush key from a seed.
//
// The seed is 32 random bytes, written as 52 lowercase base32 characters so it
// can be pasted between machines. An ed25519 private key is exactly a 32-byte
// seed, so the same seed yields the same key everywhere, with no KDF: the seed
// already carries the key's full 256 bits.
package psk

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

const comment = "ush-psk"

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSeed returns a fresh random seed as text.
func NewSeed() (string, error) {
	b := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return EncodeSeed(b), nil
}

// ParseSeed decodes seed text. Case, whitespace and dashes are ignored, so a
// grouped or re-typed seed still parses.
func ParseSeed(text string) ([]byte, error) {
	clean := strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, strings.ToUpper(text))
	if clean == "" {
		return nil, errors.New("the seed is empty")
	}
	b, err := encoding.DecodeString(clean)
	if err != nil {
		return nil, errors.New("the seed is not valid base32 (letters a-z and digits 2-7)")
	}
	if len(b) != ed25519.SeedSize {
		return nil, fmt.Errorf("the seed decodes to %d bytes, want %d (52 characters)", len(b), ed25519.SeedSize)
	}
	return b, nil
}

// EncodeSeed writes seed bytes as the text NewSeed produces.
func EncodeSeed(b []byte) string {
	return strings.ToLower(encoding.EncodeToString(b))
}

// SeedFromKeyFile returns the seed of the ed25519 private key at path. An
// ed25519 private key is its seed, so this works for any such key, derived
// from a seed or not; ush pair uses it to hand the key to another device.
func SeedFromKeyFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading key: %w", err)
	}
	raw, err := ssh.ParseRawPrivateKey(b)
	var locked *ssh.PassphraseMissingError
	if errors.As(err, &locked) {
		return nil, fmt.Errorf("%s is protected by a passphrase; ush needs an unencrypted key (ush init --seed writes one)", path)
	}
	if err != nil {
		return nil, fmt.Errorf("reading key %s: %w", path, err)
	}
	switch k := raw.(type) {
	case *ed25519.PrivateKey:
		return k.Seed(), nil
	case ed25519.PrivateKey:
		return k.Seed(), nil
	}
	return nil, fmt.Errorf("%s is not an ed25519 key; pairing needs the key ush init writes", path)
}

// FromSeed returns the key for a 32-byte seed.
func FromSeed(seed []byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(seed)
}

// HostKey derives a server host key for purpose ("sshd", "uptermd") from the
// seed, so a rendezvous keeps the same host keys across rebuilds and clients'
// known_hosts entries stay valid. HKDF with a per-purpose label keeps each one
// distinct from the login key and from the others.
func HostKey(seed []byte, purpose string) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(hostSeed(seed, purpose))
}

func hostSeed(seed []byte, purpose string) []byte {
	b, err := hkdf.Key(sha256.New, seed, nil, "ush/host-key/"+purpose, ed25519.SeedSize)
	if err != nil {
		panic(err) // only for impossible lengths
	}
	return b
}

// Write stores key at path (0600) and its public half at path.pub, commented
// ush-psk. It never replaces a different key: if path already holds this key
// it only restores a missing .pub, and if it holds another key it fails.
func Write(path string, key ed25519.PrivateKey) (created bool, err error) {
	return WriteComment(path, key, comment)
}

// WriteComment is Write with a chosen comment on the key.
func WriteComment(path string, key ed25519.PrivateKey, comment string) (created bool, err error) {
	pub, err := ssh.NewPublicKey(key.Public())
	if err != nil {
		return false, err
	}
	pubLine := append(bytes.TrimRight(ssh.MarshalAuthorizedKey(pub), "\n"), " "+comment+"\n"...)

	existing, err := os.ReadFile(path)
	switch {
	case err == nil:
		signer, err := ssh.ParsePrivateKey(existing)
		if err != nil {
			return false, fmt.Errorf("%s exists and is not a readable key: %w", path, err)
		}
		if !bytes.Equal(signer.PublicKey().Marshal(), pub.Marshal()) {
			return false, fmt.Errorf("%s already holds a different key; move it aside or use another path", path)
		}
		if _, err := os.Stat(path + ".pub"); errors.Is(err, fs.ErrNotExist) {
			return false, os.WriteFile(path+".pub", pubLine, 0o644)
		}
		return false, nil
	case !errors.Is(err, fs.ErrNotExist):
		return false, err
	}

	block, err := ssh.MarshalPrivateKey(key, comment)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, err
	}
	if _, err := f.Write(pem.EncodeToMemory(block)); err != nil {
		f.Close()
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	return true, os.WriteFile(path+".pub", pubLine, 0o644)
}
