package hostkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func newKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

var remote = &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 7874}

func TestFirstUseRecordsTheKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not", "yet", "known_hosts") // the directory does not exist
	cb, err := TOFU(path)
	if err != nil {
		t.Fatal(err)
	}
	key := newKey(t)
	if err := cb("rv.example:7874", remote, key); err != nil {
		t.Fatalf("an unknown host must be accepted on first use: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "[rv.example]:7874 ") {
		t.Fatalf("known_hosts = %q, want the host recorded", body)
	}
	if strings.Contains(string(body), "192.0.2.1") {
		t.Fatal("only the name should be recorded, not the address")
	}
	// The same key again, from a fresh callback reading the file: fine.
	cb2, err := TOFU(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cb2("rv.example:7874", remote, key); err != nil {
		t.Fatalf("a recorded key must be accepted: %v", err)
	}
}

func TestAChangedKeyIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	cb, err := TOFU(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cb("rv.example:7874", remote, newKey(t)); err != nil {
		t.Fatal(err)
	}
	cb, err = TOFU(path)
	if err != nil {
		t.Fatal(err)
	}
	err = cb("rv.example:7874", remote, newKey(t))
	if err == nil || !strings.Contains(err.Error(), "changed") || !strings.Contains(err.Error(), path+":1") {
		t.Fatalf("a different key must be refused, naming the file and line: %v", err)
	}
}

func TestOneCallbackSeesWhatItRecorded(t *testing.T) {
	// The registration reconnects with the same callback; the key it
	// recorded on the first connection must bind the second.
	path := filepath.Join(t.TempDir(), "known_hosts")
	cb, err := TOFU(path)
	if err != nil {
		t.Fatal(err)
	}
	first := newKey(t)
	if err := cb("rv.example:7874", remote, first); err != nil {
		t.Fatal(err)
	}
	if err := cb("rv.example:7874", remote, newKey(t)); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("a different key on the same callback must be refused: %v", err)
	}
	if err := cb("rv.example:7874", remote, first); err != nil {
		t.Fatalf("the recorded key must still be accepted: %v", err)
	}
	body, _ := os.ReadFile(path)
	if n := strings.Count(string(body), "\n"); n != 1 {
		t.Fatalf("known_hosts has %d lines, want exactly one:\n%s", n, body)
	}
}

func TestAReadOnlyKnownHostsThatAlreadyRecordsTheHostIsAccepted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: 0444 does not stop root from writing")
	}
	path := filepath.Join(t.TempDir(), "known_hosts")
	key := newKey(t)
	if err := os.WriteFile(path, []byte(knownhosts.Line([]string{"rv.example:7874"}, key)+"\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	cb, err := TOFU(path)
	if err != nil {
		t.Fatalf("TOFU on a read-only known_hosts must not fail: %v", err)
	}
	if err := cb("rv.example:7874", remote, key); err != nil {
		t.Fatalf("a recorded host on a read-only file must be accepted: %v", err)
	}
}

func TestAppendAddsAMissingTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	// A hand-edited file with no trailing newline.
	line := knownhosts.Line([]string{"one.example:7874"}, newKey(t))
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	cb, err := TOFU(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cb("two.example:7874", remote, newKey(t)); err != nil {
		t.Fatalf("appending after a file with no trailing newline must not fail: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("known_hosts has %d lines, want 2 (the new entry must not be glued onto the old line):\n%s", len(lines), body)
	}
	if !strings.Contains(lines[0], "one.example") || !strings.Contains(lines[1], "two.example") {
		t.Fatalf("known_hosts = %q, want the old and new lines kept apart", body)
	}
}

func TestHostsAreIndependent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	cb, err := TOFU(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cb("one.example:7874", remote, newKey(t)); err != nil {
		t.Fatal(err)
	}
	if err := cb("two.example:7874", remote, newKey(t)); err != nil {
		t.Fatalf("a second host with its own key is not a change: %v", err)
	}
}
