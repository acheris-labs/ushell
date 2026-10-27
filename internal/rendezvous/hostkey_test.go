package rendezvous

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"

	"golang.org/x/crypto/ssh"
)

func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The daemon's own host key is pinned outright in the session child: it is
// derived from the seed the daemon has, so there is nothing to discover.
func TestPinnedHostKey(t *testing.T) {
	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7874}
	host := newSigner(t)
	line := string(ssh.MarshalAuthorizedKey(host.PublicKey()))

	cb, err := pinnedHostKey(line)
	if err != nil {
		t.Fatal(err)
	}
	if err := cb("ushd", addr, host.PublicKey()); err != nil {
		t.Fatalf("the derived key should be accepted: %v", err)
	}
	if err := cb("ushd", addr, newSigner(t).PublicKey()); err == nil {
		t.Fatal("any other key must be rejected")
	}
	if _, err := pinnedHostKey("not a key"); err == nil {
		t.Fatal("an unparseable pin must fail rather than accept everything")
	}
}
