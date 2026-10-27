package sharer

import (
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"

	"ush/internal/termio"
	"ush/internal/tunnel"
)

// viewer is one attached terminal. In guest-command mode it owns a pty of
// its own; otherwise it is one of the windows on the shared pty.
type viewer struct {
	s     *Session
	id    string
	guest *tunnel.Guest

	mu       sync.Mutex
	size     tunnel.Size
	sized    bool     // size has been set by a window-change; the pty-req's size must not overwrite it
	own      *os.File // the guest command's pty, once started
	attached bool     // counted among the shared pty's windows
}

// Accept serves a channel the rendezvous opened. It returns at once; the
// viewer is served on a goroutine of its own, and parked until Run has a
// pty if it arrives early.
func (s *Session) Accept(nc ssh.NewChannel) {
	go s.serve(nc)
}

func (s *Session) serve(nc ssh.NewChannel) {
	v := &viewer{s: s, id: s.viewerID()}
	g, err := tunnel.Accept(nc, v.resize)
	if err != nil {
		s.o.Logger.Warn("viewer refused", "error", err)
		return
	}
	v.guest = g
	v.setInitialSize(g.Size)

	select {
	case <-s.started:
	case <-s.done:
		_ = g.Close()
		return
	}
	if !s.track(v) {
		_ = g.Close()
		return
	}
	defer s.untrack(v)
	defer g.Close()

	if len(s.o.GuestCommand) > 0 {
		v.runGuestCommand()
		return
	}

	// Joining and leaving the window set happen under v.mu, the lock a
	// resize takes too, so a resize can neither be lost between the two
	// steps of joining nor put back a viewer that has just left. (A resize
	// can still be delivered after the channel closes: requests queue.)
	v.mu.Lock()
	v.attached = true
	s.win.set(v.id, v.size)
	v.mu.Unlock()
	s.applySize()
	defer func() {
		v.mu.Lock()
		v.attached = false
		s.win.remove(v.id)
		v.mu.Unlock()
		s.applySize()
	}()

	// The viewer gets output through a sink of its own, so it can never hold
	// up the sharer's terminal or the other viewers; if it falls too far
	// behind it is dropped. Terminal queries are filtered out on the way, so
	// the viewer's terminal does not answer questions meant for the sharer's.
	sink := termio.NewAsyncWriter(termio.NewTerminalQueryFilter(g), termio.DefaultGuestBufferSize, func(err error) {
		s.o.Logger.Warn("dropping a viewer that cannot keep up", "viewer", v.id, "error", err)
		_ = g.Close()
	})
	if err := s.out.Append(sink); err != nil {
		_ = sink.Close() // the session is ending; there is nothing to show
		return
	}
	defer func() {
		s.out.Remove(sink)
		_ = sink.Close()
	}()

	if s.o.ReadOnly {
		_, _ = io.WriteString(sink, readOnlyBanner)
		_, _ = io.Copy(io.Discard, g) // reading is how we learn the viewer left
		return
	}
	_, _ = io.Copy(s.pty, g)
}

// setInitialSize records the size from the viewer's pty-req, unless a
// window-change has already set one.
//
// tunnel.Accept's request goroutine keeps running after Accept returns, so a
// window-change that arrives hard on the heels of the pty-req can call
// resize before serve gets here; without this guard that later, better size
// would be overwritten by the pty-req's now-stale one.
func (v *viewer) setInitialSize(size tunnel.Size) {
	v.mu.Lock()
	if !v.sized {
		v.size = size
	}
	v.mu.Unlock()
}

// resize is called from the channel's request goroutine on every
// window-change.
func (v *viewer) resize(size tunnel.Size) {
	v.mu.Lock()
	v.size = size
	v.sized = true
	if v.own != nil {
		// Under v.mu, which runGuestCommand also holds while it forgets the
		// pty before closing it, so this never resizes a closed descriptor.
		_ = pty.Setsize(v.own, winsize(size))
		v.mu.Unlock()
		return
	}
	attached := v.attached
	if attached {
		v.s.win.set(v.id, size)
	}
	v.mu.Unlock()
	if attached && !v.s.applySize() {
		v.s.repaint()
	}
}

// runGuestCommand gives this viewer a command of its own, on a pty sized to
// its terminal, with TERM from its pty-req. It ends when the command exits
// or the viewer leaves, whichever is first. The shared pty and its window
// set are not involved.
func (v *viewer) runGuestCommand() {
	s, g := v.s, v.guest
	cmd := exec.Command(s.o.GuestCommand[0], s.o.GuestCommand[1:]...)
	cmd.Env = append(os.Environ(), "TERM="+g.Term)
	v.mu.Lock()
	size := v.size
	v.mu.Unlock()
	ptmx, err := pty.StartWithSize(cmd, winsize(size))
	if err != nil {
		s.o.Logger.Warn("guest command failed to start", "error", err)
		return
	}
	v.mu.Lock()
	v.own = ptmx
	// A resize can have arrived between reading size above and starting the
	// command: it only updated v.size, since own was still nil then. Re-apply
	// the latest size now that there is a pty to apply it to, so that resize
	// is not stuck at the command's start size until the next one.
	_ = pty.Setsize(ptmx, winsize(v.size))
	v.mu.Unlock()

	// Read-only applies here too (herdr has no read-only client of its own):
	// the banner first, and the viewer's keystrokes are read, which is how
	// we learn it left, and discarded.
	if s.o.ReadOnly {
		_, _ = io.WriteString(g, readOnlyBanner)
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	readCtx, stopRead := context.WithCancel(context.Background())
	defer stopRead()
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		_, _ = io.Copy(g, termio.NewContextReader(readCtx, ptmx))
	}()
	left := make(chan struct{})
	go func() {
		defer close(left)
		if s.o.ReadOnly {
			_, _ = io.Copy(io.Discard, g)
			return
		}
		_, _ = io.Copy(ptmx, g)
	}()

	select {
	case <-exited:
	case <-left:
		_ = cmd.Process.Kill()
		<-exited
	}
	select {
	case <-copied:
	case <-time.After(drainTimeout):
	}
	stopRead()
	<-copied
	v.mu.Lock()
	v.own = nil // a late resize must not touch a closed pty
	_ = ptmx.Close()
	v.mu.Unlock()
}
