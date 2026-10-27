package sharer

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"

	"ush/internal/tunnel"
	"ush/internal/tunnel/tunneltest"
)

// devNull is a file that is not a terminal, for the sharer's stdin and stdout.
func devNull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// stream reads r on a goroutine and keeps everything it has seen, so a test
// can wait for one string and then the next without losing the bytes
// between them, and without two goroutines touching one buffer unlocked.
type stream struct {
	mu   sync.Mutex
	got  strings.Builder
	done chan struct{}
}

func newStream(r io.Reader) *stream {
	s := &stream{done: make(chan struct{})}
	go func() {
		defer close(s.done)
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			s.mu.Lock()
			s.got.Write(buf[:n])
			s.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return s
}

func (s *stream) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.got.String()
}

// waitFor returns everything read so far once it contains want.
func (s *stream) waitFor(t *testing.T, want string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if text := s.text(); strings.Contains(text, want) {
			return text
		}
		select {
		case <-s.done:
			// The last bytes and the end can land together: look once more.
			if text := s.text(); strings.Contains(text, want) {
				return text
			}
			t.Fatalf("the stream ended with %q, want it to contain %q", s.text(), want)
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("read %q, want it to contain %q", s.text(), want)
	return ""
}

// waitClosed fails unless the stream ends soon: the viewer's channel closed.
func (s *stream) waitClosed(t *testing.T) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatalf("the viewer's channel stayed open; read so far: %q", s.text())
	}
}

// harness is a Session wired to the sharer's end of an SSH connection, and
// the rendezvous's end, from which viewers are opened.
type harness struct {
	s      *Session
	server *ssh.ServerConn
	runErr chan error
}

func start(t *testing.T, o Options) *harness {
	t.Helper()
	if o.Stdin == nil {
		o.Stdin = devNull(t)
	}
	if o.Stdout == nil {
		o.Stdout = devNull(t)
	}
	server, _, client := tunneltest.Pair(t)
	s := New(o)
	viewers := client.HandleChannelOpen(tunnel.ChannelType)
	go func() {
		for nc := range viewers {
			s.Accept(nc)
		}
	}()
	h := &harness{s: s, server: server, runErr: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { h.runErr <- s.Run(ctx) }()
	return h
}

func (h *harness) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-h.s.started:
	case err := <-h.runErr:
		t.Fatalf("Run returned before starting: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the command never started")
	}
}

// viewer opens a viewer with an xterm of the given size, and returns its
// channel and a stream of what it is shown.
func (h *harness) viewer(t *testing.T, size tunnel.Size) (*tunnel.Viewer, *stream) {
	t.Helper()
	v, err := tunnel.Dial(h.server, "", "xterm", size)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	return v, newStream(v)
}

func waitSize(t *testing.T, s *Session, cols, rows uint16) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ws, err := pty.GetsizeFull(s.pty)
		if err == nil && ws.Cols == cols && ws.Rows == rows {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	ws, _ := pty.GetsizeFull(s.pty)
	t.Fatalf("pty is %dx%d, want %dx%d", ws.Cols, ws.Rows, cols, rows)
}

func TestOutputReachesAViewerAndInputReachesTheCommand(t *testing.T) {
	h := start(t, Options{Command: []string{"/bin/cat"}})
	h.waitStarted(t)
	v, out := h.viewer(t, tunnel.Size{Cols: 80, Rows: 24})
	if _, err := v.Write([]byte("hello\r")); err != nil {
		t.Fatal(err)
	}
	// The pty echoes the keystrokes and cat writes the line back.
	out.waitFor(t, "hello\r\nhello")
}

func TestViewerArrivingBeforeRunIsServed(t *testing.T) {
	// Open the viewer before Run has a pty: it must be parked, not lost.
	server, _, client := tunneltest.Pair(t)
	s := New(Options{Command: []string{"/bin/cat"}, Stdin: devNull(t), Stdout: devNull(t)})
	viewers := client.HandleChannelOpen(tunnel.ChannelType)
	go func() {
		for nc := range viewers {
			s.Accept(nc)
		}
	}()
	type dialed struct {
		v   *tunnel.Viewer
		err error
	}
	dial := make(chan dialed, 1)
	go func() {
		v, err := tunnel.Dial(server, "", "xterm", tunnel.Size{Cols: 80, Rows: 24})
		dial <- dialed{v, err}
	}()
	d := <-dial // Dial returns once the pty-req is answered, which happens before Run
	if d.err != nil {
		t.Fatal(d.err)
	}
	t.Cleanup(func() { _ = d.v.Close() })
	out := newStream(d.v)
	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = s.Run(ctx) }()

	if _, err := d.v.Write([]byte("early\r")); err != nil {
		t.Fatal(err)
	}
	out.waitFor(t, "early\r\nearly")
}

func TestAResizeThatLeavesTheSizeAloneStillRepaints(t *testing.T) {
	// A viewer bigger than the smallest terminal asks for a repaint by
	// nudging its size (the join does this, and so does Ctrl-] r). The pty
	// size does not move, but the program must still hear about it, or the
	// viewer sees only what changes from then on.
	h := start(t, Options{Command: []string{"/bin/sh", "-c", `trap 'echo WINCH' WINCH; echo ready; while :; do sleep 0.05; done`}})
	h.waitStarted(t)
	_, small := h.viewer(t, tunnel.Size{Cols: 80, Rows: 24})
	small.waitFor(t, "ready")
	big, _ := h.viewer(t, tunnel.Size{Cols: 120, Rows: 40})
	waitSize(t, h.s, 80, 24)
	seen := strings.Count(small.text(), "WINCH")

	if err := big.Resize(tunnel.Size{Cols: 120, Rows: 39}); err != nil {
		t.Fatal(err)
	}
	if err := big.Resize(tunnel.Size{Cols: 120, Rows: 40}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(small.text(), "WINCH") == seen {
		if time.Now().After(deadline) {
			t.Fatalf("the program never heard the nudge; read %q", small.text())
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitSize(t, h.s, 80, 24) // and the smallest terminal still wins
}

func TestTheSmallestTerminalSetsThePtySize(t *testing.T) {
	h := start(t, Options{Command: []string{"/bin/cat"}})
	h.waitStarted(t)
	big, _ := h.viewer(t, tunnel.Size{Cols: 200, Rows: 60})
	small, _ := h.viewer(t, tunnel.Size{Cols: 80, Rows: 24})
	waitSize(t, h.s, 80, 24)

	_ = small.Close()
	waitSize(t, h.s, 200, 60)

	if err := big.Resize(tunnel.Size{Cols: 100, Rows: 30}); err != nil {
		t.Fatal(err)
	}
	waitSize(t, h.s, 100, 30)
}

func TestReadOnlyShowsABannerAndDropsInput(t *testing.T) {
	h := start(t, Options{Command: []string{"/bin/cat"}, ReadOnly: true})
	h.waitStarted(t)
	v, out := h.viewer(t, tunnel.Size{Cols: 80, Rows: 24})
	out.waitFor(t, "=== Attached to read-only session ===")
	if _, err := v.Write([]byte("typed\r")); err != nil {
		t.Fatal(err)
	}
	// Typing as the sharer proves the pty is live, and fixes the order of
	// events: had the viewer's keystrokes reached the pty, their echo would
	// have arrived before the marker's.
	time.Sleep(100 * time.Millisecond)
	if _, err := h.s.pty.Write([]byte("marker\r")); err != nil {
		t.Fatal(err)
	}
	got := out.waitFor(t, "marker\r\nmarker")
	if strings.Contains(got, "typed") {
		t.Fatalf("read-only input reached the command: %q", got)
	}
}

func TestGuestCommandRunsPerViewerWithItsOwnTerminal(t *testing.T) {
	h := start(t, Options{
		Command:      []string{"/bin/cat"},
		GuestCommand: []string{"/bin/sh", "-c", "echo guest:$TERM; stty size; exec cat"},
	})
	h.waitStarted(t)
	v, out := h.viewer(t, tunnel.Size{Cols: 90, Rows: 30})
	out.waitFor(t, "guest:xterm")
	out.waitFor(t, "30 90") // stty prints rows then cols
	if _, err := v.Write([]byte("own\r")); err != nil {
		t.Fatal(err)
	}
	out.waitFor(t, "own\r\nown")
	// The shared pty was never resized for this viewer.
	ws, err := pty.GetsizeFull(h.s.pty)
	if err != nil {
		t.Fatal(err)
	}
	if ws.Cols == 90 {
		t.Fatal("a guest command viewer must not size the shared pty")
	}
}

func TestReadOnlyAppliesToGuestCommandsToo(t *testing.T) {
	// herdr has no read-only client of its own, so ush share -k --read-only
	// under it depends on this.
	h := start(t, Options{
		Command:      []string{"/bin/cat"},
		GuestCommand: []string{"/bin/sh", "-c", "echo ready; exec cat"},
		ReadOnly:     true,
	})
	h.waitStarted(t)
	v, out := h.viewer(t, tunnel.Size{Cols: 80, Rows: 24})
	out.waitFor(t, "=== Attached to read-only session ===")
	out.waitFor(t, "ready")
	if _, err := v.Write([]byte("typed\r")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // the echo would have arrived by now
	if got := out.text(); strings.Contains(got, "typed") {
		t.Fatalf("read-only input reached the guest command: %q", got)
	}
}

func TestCommandExitClosesViewers(t *testing.T) {
	h := start(t, Options{Command: []string{"/bin/sh", "-c", "echo ready; read x; echo bye"}})
	h.waitStarted(t)
	v, out := h.viewer(t, tunnel.Size{Cols: 80, Rows: 24})
	out.waitFor(t, "ready")
	if _, err := v.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	out.waitFor(t, "bye")
	out.waitClosed(t)
	select {
	case err := <-h.runErr:
		if err != nil {
			t.Fatalf("a command that exits 0 is not an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the command exited")
	}
}

func TestCancelKillsTheCommand(t *testing.T) {
	_, _, client := tunneltest.Pair(t)
	s := New(Options{Command: []string{"/bin/sleep", "60"}, Stdin: devNull(t), Stdout: devNull(t)})
	viewers := client.HandleChannelOpen(tunnel.ChannelType)
	go func() {
		for nc := range viewers {
			s.Accept(nc)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	<-s.started
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// TestAParkedViewerIsClosedWhenRunFailsBeforeStarting covers the failure-mode
// table row "a viewer that arrives early is parked": if Run never gets a pty
// (here, an empty Command), the viewer parked in serve's select on s.started
// vs. s.done must not be left hanging forever.
func TestAParkedViewerIsClosedWhenRunFailsBeforeStarting(t *testing.T) {
	server, _, client := tunneltest.Pair(t)
	s := New(Options{Stdin: devNull(t), Stdout: devNull(t)}) // no Command: Run fails at once
	viewers := client.HandleChannelOpen(tunnel.ChannelType)
	go func() {
		for nc := range viewers {
			s.Accept(nc)
		}
	}()

	v, err := tunnel.Dial(server, "", "xterm", tunnel.Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	out := newStream(v)

	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run with no Command must return an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	// The parked viewer's channel must be closed, not left open, once Run
	// gives up without ever reaching s.started.
	out.waitClosed(t)
}

// TestDrainStaysBoundedWithABackgroundChildHoldingThePty covers the
// failure-mode table row "drain is bounded even with a background child
// holding the pty": a grandchild that keeps the pty's write end open must
// not stop Run from returning once the foreground command exits.
func TestDrainStaysBoundedWithABackgroundChildHoldingThePty(t *testing.T) {
	h := start(t, Options{Command: []string{"/bin/sh", "-c", "sleep 30 & echo x"}})
	select {
	case err := <-h.runErr:
		if err != nil {
			t.Fatalf("a command that exits 0 is not an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within a few seconds; the drain is not bounded")
	}
}

// TestAResizeThatBeatsThePtyReqSizeIsKept drives serve's ordering directly,
// with no goroutines or timing: tunnel.Accept's request goroutine can call
// resize before serve reaches its own assignment of the pty-req's size (the
// window between tunnel.Accept returning and serve setting the size from
// g.Size). A resize arriving there must win, not be clobbered by the
// now-stale pty-req size that arrives after it.
func TestAResizeThatBeatsThePtyReqSizeIsKept(t *testing.T) {
	v := &viewer{}
	v.resize(tunnel.Size{Cols: 100, Rows: 40})        // the window-change, arriving first
	v.setInitialSize(tunnel.Size{Cols: 80, Rows: 24}) // the pty-req's size, applied after

	v.mu.Lock()
	got := v.size
	v.mu.Unlock()
	if got != (tunnel.Size{Cols: 100, Rows: 40}) {
		t.Fatalf("setInitialSize overwrote a resize that arrived first: got %+v", got)
	}
}
