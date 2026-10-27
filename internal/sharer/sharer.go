// Package sharer runs a command on a pty and serves it to viewers arriving as
// tunnel channels: the sharer's half of ush, in place of upterm's host.
package sharer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"

	"ush/internal/termio"
	"ush/internal/tunnel"
)

const (
	// replayWrites is how many of the last pty writes a joining viewer gets,
	// so it sees a prompt rather than a blank screen.
	replayWrites = 5
	// drainTimeout bounds how long, after the command exits, output still
	// sitting in the pty is waited for. On Unix the read ends on its own once
	// the last process holding the pty closes it; a background child that
	// keeps it open is why this is bounded.
	drainTimeout = time.Second
	// flushTimeout bounds how long viewers get to receive the last output.
	flushTimeout = time.Second
	// readOnlyBanner is what a viewer of a read-only session sees first.
	readOnlyBanner = "\r\n=== Attached to read-only session ===\r\n\r\n"
	// repaintPause is how long the pty stays a row short when repaint nudges
	// it: long enough that the program reads the short size before it is
	// put back, so it redraws rather than seeing nothing changed.
	repaintPause = 120 * time.Millisecond
)

// Options say what to share and to whom.
type Options struct {
	Command []string // what runs on the shared pty
	Env     []string // added to this process's environment for Command
	// GuestCommand, if set, runs once per viewer on a pty of that viewer's
	// own, instead of the viewer watching Command's pty. Keep mode uses it
	// to give each viewer its own multiplexer client.
	GuestCommand []string
	ReadOnly     bool // viewers watch; their input is discarded
	// Adopt, if set, is a running process to move onto the shared pty in
	// place of starting Command. It is not killed when the session ends.
	Adopt  int
	Stdin  *os.File // the sharer's terminal, or something that is not one
	Stdout *os.File // where the pty's output also goes
	Logger *slog.Logger
}

// Session is one shared command and its viewers.
type Session struct {
	o   Options
	out *termio.MultiWriter
	win *windows

	started chan struct{} // closed once pty is set
	done    chan struct{} // closed once the session is over
	pty     *os.File

	// ptyMu orders resizes against the close at the end: Setsize reads the
	// descriptor, and a viewer's window-change can arrive at any moment.
	// applied is the size last set, so a resize that changes nothing is known.
	ptyMu      sync.Mutex
	ptyGone    bool
	applied    tunnel.Size
	repainting atomic.Bool

	mu      sync.Mutex
	viewers map[*viewer]struct{}
	next    int
}

// New prepares a session. Nothing runs until Run.
func New(o Options) *Session {
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	return &Session{
		o:       o,
		out:     termio.NewMultiWriter(replayWrites),
		win:     newWindows(),
		started: make(chan struct{}),
		done:    make(chan struct{}),
		viewers: make(map[*viewer]struct{}),
	}
}

// Run starts the command on a pty and serves it until it exits or ctx is
// done, in which case the command is killed. The sharer's terminal, if Stdin
// is one, is put in raw mode for the duration and its keystrokes go to the
// pty. A non-zero exit status is the command's business, not an error here.
func (s *Session) Run(ctx context.Context) (err error) {
	defer func() {
		if err != nil && s.pty == nil {
			close(s.done) // never started: release anyone parked in Accept
		}
	}()
	if len(s.o.Command) == 0 && s.o.Adopt == 0 {
		return errors.New("nothing to share")
	}
	local, isTerminal := terminalSize(s.o.Stdin)
	var ptmx *os.File
	var cmd *exec.Cmd
	wait := func() error { return waitPid(s.o.Adopt) }
	if s.o.Adopt != 0 {
		if ptmx, err = adopt(s.o.Adopt, local, isTerminal); err != nil {
			return err
		}
	} else {
		cmd = exec.Command(s.o.Command[0], s.o.Command[1:]...)
		cmd.Env = append(os.Environ(), s.o.Env...)
		if isTerminal {
			ptmx, err = pty.StartWithSize(cmd, winsize(local))
		} else {
			ptmx, err = pty.Start(cmd)
		}
		if err != nil {
			return fmt.Errorf("starting %s: %w", s.o.Command[0], err)
		}
		wait = cmd.Wait
	}
	s.pty = ptmx
	if isTerminal {
		s.win.set("local", local)
	}
	close(s.started)

	if isTerminal {
		fd := int(s.o.Stdin.Fd())
		if state, err := term.MakeRaw(fd); err == nil {
			defer func() { _ = term.Restore(fd, state) }()
		}
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer func() {
			signal.Stop(winch)
			close(winch)
		}()
		go func() {
			for range winch {
				if size, ok := terminalSize(s.o.Stdin); ok {
					s.win.set("local", size)
					s.applySize()
				}
			}
		}()
	}

	// Only the sharer's own terminal is read: a pipe or /dev/null as stdin
	// (the -t keeper) would either never end or end at once, and the command
	// has its own pty to read from.
	inputCtx, stopInput := context.WithCancel(context.Background())
	defer stopInput()
	if isTerminal {
		go func() { _, _ = io.Copy(ptmx, termio.NewContextReader(inputCtx, s.o.Stdin)) }()
	}

	if err := s.out.Append(s.o.Stdout); err != nil {
		if cmd != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		return err
	}

	exited := make(chan error, 1)
	go func() { exited <- wait() }()

	readCtx, stopRead := context.WithCancel(context.Background())
	defer stopRead()
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		_, _ = io.Copy(s.out, termio.NewContextReader(readCtx, ptmx))
	}()

	var runErr error
	select {
	case <-ctx.Done():
		if cmd != nil {
			_ = cmd.Process.Kill()
			<-exited
		}
		runErr = ctx.Err()
	case werr := <-exited:
		var exit *exec.ExitError
		if werr != nil && !errors.As(werr, &exit) {
			runErr = werr
		}
	}

	// Let the pty drain, then stop reading whatever is left.
	select {
	case <-copied:
	case <-time.After(drainTimeout):
	}
	stopRead()
	<-copied

	flushCtx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	if err := s.out.Shutdown(flushCtx); err != nil {
		s.o.Logger.Warn("gave up delivering the last output to a viewer", "error", err)
	}
	close(s.done)
	s.closeViewers()
	s.ptyMu.Lock()
	s.ptyGone = true
	_ = ptmx.Close()
	s.ptyMu.Unlock()
	return runErr
}

// applySize resizes the shared pty to fit every terminal on it. The minimum
// is taken under the same lock as the resize, so two calls cannot apply
// their sizes in the wrong order, and neither can run after the close. It
// reports whether the size moved, which is what makes the program redraw.
func (s *Session) applySize() (changed bool) {
	s.ptyMu.Lock()
	defer s.ptyMu.Unlock()
	if s.ptyGone {
		return false
	}
	size, ok := s.win.min()
	if !ok || size == s.applied {
		return false
	}
	_ = pty.Setsize(s.pty, winsize(size))
	s.applied = size
	s.winch()
	return true
}

// repaint makes the program on the shared pty redraw, leaving it the size it
// was: a row short, then back. A viewer bigger than the smallest terminal
// asks for a redraw by nudging its own size (joining does, and Ctrl-] r),
// which moves nothing here, so without this the program never hears of it
// and the viewer sees only what changes from then on. Nudges that arrive
// while one is under way are the same request.
func (s *Session) repaint() {
	if !s.repainting.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer s.repainting.Store(false)
		s.ptyMu.Lock()
		size, ok := s.win.min()
		if s.ptyGone || !ok || size.Rows < 2 {
			s.ptyMu.Unlock()
			return
		}
		short := tunnel.Size{Cols: size.Cols, Rows: size.Rows - 1}
		_ = pty.Setsize(s.pty, winsize(short))
		s.applied = short
		s.winch()
		s.ptyMu.Unlock()
		time.Sleep(repaintPause)
		s.applySize()
	}()
}

// winch tells an adopted process its terminal changed size. The pty cannot:
// it signals its foreground process group, and the process is not in one.
func (s *Session) winch() {
	if s.o.Adopt != 0 {
		_ = syscall.Kill(s.o.Adopt, syscall.SIGWINCH)
	}
}

// track remembers a viewer so it can be closed at the end. It refuses once
// the session is over.
func (s *Session) track(v *viewer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.done:
		return false
	default:
	}
	s.viewers[v] = struct{}{}
	return true
}

func (s *Session) untrack(v *viewer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.viewers, v)
}

func (s *Session) closeViewers() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for v := range s.viewers {
		_ = v.guest.Close()
	}
}

func (s *Session) viewerID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	return fmt.Sprintf("viewer-%d", s.next)
}

// terminalSize is f's size if it is a terminal.
func terminalSize(f *os.File) (tunnel.Size, bool) {
	if f == nil || !term.IsTerminal(int(f.Fd())) {
		return tunnel.Size{}, false
	}
	cols, rows, err := term.GetSize(int(f.Fd()))
	if err != nil {
		return tunnel.Size{}, false
	}
	return tunnel.Size{Cols: uint32(cols), Rows: uint32(rows)}, true
}

func winsize(s tunnel.Size) *pty.Winsize {
	return &pty.Winsize{Rows: uint16(s.Rows), Cols: uint16(s.Cols)}
}
