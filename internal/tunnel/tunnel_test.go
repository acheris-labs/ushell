package tunnel

import (
	"errors"
	"io"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"ush/internal/tunnel/tunneltest"
)

// readN reads exactly n bytes or fails the test.
func readN(t *testing.T, r io.Reader, n int) string {
	t.Helper()
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("reading %d bytes: %v", n, err)
	}
	return string(buf)
}

func TestPtyRequestReachesTheSharerAndBytesFlowBothWays(t *testing.T) {
	server, _, client := tunneltest.Pair(t)
	// The sharer (client side) must be listening before the channel is opened.
	viewers := client.HandleChannelOpen(ChannelType)

	type dialed struct {
		v   *Viewer
		err error
	}
	dial := make(chan dialed, 1)
	go func() {
		v, err := Dial(server, "AbCdEfGhIjKlMnOpQrSt", "xterm-256color", Size{Cols: 120, Rows: 40})
		dial <- dialed{v, err}
	}()

	resized := make(chan Size, 1)
	g, err := Accept(<-viewers, func(s Size) { resized <- s })
	if err != nil {
		t.Fatal(err)
	}
	if g.Term != "xterm-256color" || g.Size != (Size{Cols: 120, Rows: 40}) {
		t.Fatalf("sharer saw term %q size %+v", g.Term, g.Size)
	}
	d := <-dial
	if d.err != nil {
		t.Fatal(d.err)
	}
	v := d.v

	if _, err := v.Write([]byte("typed")); err != nil {
		t.Fatal(err)
	}
	if got := readN(t, g, 5); got != "typed" {
		t.Fatalf("sharer read %q", got)
	}
	if _, err := g.Write([]byte("output")); err != nil {
		t.Fatal(err)
	}
	if got := readN(t, v, 6); got != "output" {
		t.Fatalf("viewer read %q", got)
	}

	if err := v.Resize(Size{Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-resized:
		if s != (Size{Cols: 80, Rows: 24}) {
			t.Fatalf("resize callback got %+v", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the resize never reached the sharer")
	}

	_ = v.Close()
	if _, err := g.Read(make([]byte, 1)); err == nil {
		t.Fatal("the sharer should see the viewer leave")
	}
}

func TestOpenCarriesTheSessionID(t *testing.T) {
	server, _, client := tunneltest.Pair(t)
	viewers := client.HandleChannelOpen(ChannelType)
	go func() {
		v, err := Dial(server, "AbCdEfGhIjKlMnOpQrSt", "vt100", Size{Cols: 80, Rows: 24})
		if err == nil {
			_ = v.Close()
		}
	}()
	nc := <-viewers
	var open Open
	if err := ssh.Unmarshal(nc.ExtraData(), &open); err != nil {
		t.Fatal(err)
	}
	if open.Session != "AbCdEfGhIjKlMnOpQrSt" {
		t.Fatalf("session = %q", open.Session)
	}
	g, err := Accept(nc, func(Size) {})
	if err != nil {
		t.Fatal(err)
	}
	_ = g.Close()
}

func TestAcceptRefusesAViewerThatNeverAsksForAPty(t *testing.T) {
	server, _, client := tunneltest.Pair(t)
	viewers := client.HandleChannelOpen(ChannelType)
	go func() {
		ch, reqs, err := server.OpenChannel(ChannelType, nil)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(reqs)
		_ = ch.Close() // hangs up without a pty-req
	}()
	if _, err := Accept(<-viewers, func(Size) {}); err == nil {
		t.Fatal("a viewer with no pty-req must be refused")
	}
}

func TestDialFailsWhenTheSharerRefusesThePty(t *testing.T) {
	server, _, client := tunneltest.Pair(t)
	viewers := client.HandleChannelOpen(ChannelType)
	go func() {
		nc := <-viewers
		ch, reqs, err := nc.Accept()
		if err != nil {
			return
		}
		for r := range reqs {
			_ = r.Reply(false, nil)
		}
		_ = ch.Close()
	}()
	_, err := Dial(server, "AbCdEfGhIjKlMnOpQrSt", "vt100", Size{Cols: 80, Rows: 24})
	if err == nil || !errors.Is(err, ErrRefused) {
		t.Fatalf("want ErrRefused, got %v", err)
	}
}

// Three parties: a viewer, a rendezvous in the middle, and a sharer. The
// rendezvous only splices; the pty-req, the resize and the bytes all go
// through it untouched.
func TestSpliceCarriesRequestsAndBytesThrough(t *testing.T) {
	sharerServer, _, sharerClient := tunneltest.Pair(t)           // rendezvous ⇄ sharer
	viewerServer, viewerChans, viewerClient := tunneltest.Pair(t) // viewer ⇄ rendezvous

	// The sharer: announce the terminal type, then echo.
	viewers := sharerClient.HandleChannelOpen(ChannelType)
	resized := make(chan Size, 1)
	sharerDone := make(chan struct{})
	go func() {
		defer close(sharerDone)
		g, err := Accept(<-viewers, func(s Size) { resized <- s })
		if err != nil {
			return
		}
		_, _ = g.Write([]byte(g.Term))
		_, _ = io.Copy(g, g)
	}()

	// The rendezvous.
	go func() {
		nc := <-viewerChans
		up, upReqs, err := sharerServer.OpenChannel(ChannelType, nil)
		if err != nil {
			_ = nc.Reject(ssh.ConnectionFailed, err.Error())
			return
		}
		down, downReqs, err := nc.Accept()
		if err != nil {
			_ = up.Close()
			return
		}
		Splice(down, downReqs, up, upReqs)
	}()
	_ = viewerServer

	v, err := Dial(viewerClient, "AbCdEfGhIjKlMnOpQrSt", "vt100", Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if got := readN(t, v, 5); got != "vt100" {
		t.Fatalf("viewer read %q, want the term the sharer saw", got)
	}
	if _, err := v.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if got := readN(t, v, 4); got != "ping" {
		t.Fatalf("echo came back as %q", got)
	}
	if err := v.Resize(Size{Cols: 132, Rows: 50}); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-resized:
		if s != (Size{Cols: 132, Rows: 50}) {
			t.Fatalf("sharer got resize %+v", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the resize was not spliced through")
	}

	_ = v.Close()
	select {
	case <-sharerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the sharer did not see the viewer leave")
	}
}
