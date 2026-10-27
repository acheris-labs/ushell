// Package tunneltest builds an SSH connection over loopback, for tests of
// the code on either end of it.
package tunneltest

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Pair returns both ends of one SSH connection over loopback TCP: the
// server side, the channels it is asked to open, and a client. The server
// requires no authentication. Both are closed when the test ends. (Not
// net.Pipe: it has no buffer, and both sides of an SSH handshake write
// their version line before reading, so the handshake would deadlock.)
func Pair(t testing.TB) (server *ssh.ServerConn, serverChans <-chan ssh.NewChannel, client *ssh.Client) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	type result struct {
		conn  *ssh.ServerConn
		chans <-chan ssh.NewChannel
		reqs  <-chan *ssh.Request
		err   error
	}
	done := make(chan result, 1)
	go func() {
		a, err := ln.Accept()
		if err != nil {
			done <- result{err: err}
			return
		}
		conn, chans, reqs, err := ssh.NewServerConn(a, config)
		done <- result{conn, chans, reqs, err}
	}()
	b, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	cc, cchans, creqs, err := ssh.NewClientConn(b, "loopback", &ssh.ClientConfig{
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		t.Fatal(err)
	}
	client = ssh.NewClient(cc, cchans, creqs)
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	go ssh.DiscardRequests(r.reqs)
	t.Cleanup(func() {
		_ = client.Close()
		_ = r.conn.Close()
	})
	return r.conn, r.chans, client
}
