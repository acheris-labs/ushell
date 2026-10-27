package keep

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// ValidName is what a kept session may be called. It matches ush's own rule
// for session names, because in keep mode they are the same name.
var ValidName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

// Dir is where sockets live: one per kept session. USH_KEEP_DIR overrides it,
// which matters because a unix socket path is limited to around 100 bytes on
// macOS -- long enough for a home directory, not always for somewhere else.
func Dir() (string, error) {
	dir := os.Getenv("USH_KEEP_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".ush", "keep")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// Whoever can reach a socket here can attach to that kept shell, and
	// MkdirAll leaves a directory that already exists as it was.
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !fi.IsDir():
		return "", fmt.Errorf("%s is not a directory (a symlink?): kept sessions need a real one", dir)
	case fi.Mode().Perm()&0o077 != 0:
		return "", fmt.Errorf("%s is open to other users (%v): chmod 700 it", dir, fi.Mode().Perm())
	case ok && int(st.Uid) != os.Getuid():
		return "", fmt.Errorf("%s belongs to another user", dir)
	}
	return dir, nil
}

// SocketPath is the socket for one name.
func SocketPath(name string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".sock"), nil
}

// server is one kept session: a command on a pty, an emulator following its
// screen, and the terminals currently watching.
type server struct {
	name    string
	command []string

	ptmx *os.File
	cmd  *exec.Cmd

	mu      sync.Mutex
	screen  *vt.Emulator
	modes   map[ansi.DECMode]bool // replayed modes the program has set or reset
	clients map[*client]struct{}
}

// replayed are the modes a detaching terminal turns off (detach.ResetModes)
// that the program may be relying on: the repaint puts them back as the
// program left them, or a terminal attaching midway would, say, never report
// the mouse to a program that asked for it.
var replayed = []ansi.DECMode{
	ansi.ModeMouseNormal, ansi.ModeMouseButtonEvent, ansi.ModeMouseAnyEvent,
	ansi.ModeMouseExtUtf8, ansi.ModeMouseExtSgr, ansi.ModeMouseExtUrxvt,
	ansi.ModeFocusEvent, ansi.ModeBracketedPaste, ansi.ModeTextCursorEnable,
}

// modeSequences sets or resets each replayed mode as the program last did.
func modeSequences(modes map[ansi.DECMode]bool) string {
	var b strings.Builder
	for _, m := range replayed {
		if on, seen := modes[m]; seen {
			if on {
				b.WriteString(ansi.SetMode(m))
			} else {
				b.WriteString(ansi.ResetMode(m))
			}
		}
	}
	return b.String()
}

type client struct {
	conn     net.Conn
	readOnly bool
	mu       sync.Mutex // one writer at a time per connection
}

func (c *client) send(k kind, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return writeFrame(c.conn, k, payload)
}

// Run starts command under a pty and serves attachments until it exits. It
// returns the command's exit code, so `ush share -k` can report it the way
// running the command directly would. shown is what the session reports it
// is running (nil: command), for when command is a wrapper around it.
func Run(name string, command, shown []string, env []string, initial Size) (int, error) {
	if !ValidName.MatchString(name) {
		return 1, fmt.Errorf("invalid name %q: use 1-32 of A-Z a-z 0-9 _ -", name)
	}
	if len(command) == 0 {
		return 1, errors.New("keep: nothing to run")
	}
	sock, err := SocketPath(name)
	if err != nil {
		return 1, err
	}

	// A socket left by a keeper that is gone would otherwise refuse the
	// listen; one still answering means this name is taken.
	if conn, err := net.Dial("unix", sock); err == nil {
		conn.Close()
		return 1, fmt.Errorf("keep: %q is already running", name)
	}
	_ = os.Remove(sock)

	ln, err := net.Listen("unix", sock)
	if err != nil {
		if len(sock) > 100 {
			return 1, fmt.Errorf("keep: socket path is too long (%d bytes): set USH_KEEP_DIR to somewhere shorter", len(sock))
		}
		return 1, err
	}
	defer func() {
		_ = ln.Close()
		_ = os.Remove(sock)
	}()

	if shown == nil {
		shown = command
	}
	if initial.Width <= 0 || initial.Height <= 0 {
		initial = Size{Width: 80, Height: 24}
	}

	cmd := exec.Command(command[0], command[1:]...)
	cmd.Env = env
	// Its own process group, so stopping the session reaches whatever the
	// command started, not just the command.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Cols: uint16(initial.Width), Rows: uint16(initial.Height),
	})
	if err != nil {
		return 1, fmt.Errorf("keep: starting %s: %w", command[0], err)
	}

	s := &server{
		name:    name,
		command: shown,
		ptmx:    ptmx,
		cmd:     cmd,
		screen:  vt.NewEmulator(initial.Width, initial.Height),
		modes:   map[ansi.DECMode]bool{},
		clients: map[*client]struct{}{},
	}
	// The emulator calls these from Write, which runs under s.mu.
	record := func(on bool) func(ansi.Mode) {
		return func(m ansi.Mode) {
			if d, ok := m.(ansi.DECMode); ok && slices.Contains(replayed, d) {
				s.modes[d] = on
			}
		}
	}
	s.screen.SetCallbacks(vt.Callbacks{EnableMode: record(true), DisableMode: record(false)})

	// The emulator is this command's terminal, so it answers the questions a
	// terminal is asked -- device attributes, cursor position, in-band resize
	// -- and those answers have to reach the program as input. They arrive
	// through a pipe, and a pipe blocks until it is read: leave it undrained
	// and the first query from a full-screen program wedges the emulator, and
	// with it every attached terminal.
	go func() { _, _ = io.Copy(ptmx, s.screen) }()

	go s.accept(ln)
	s.pump() // returns when the command's pty closes

	err = cmd.Wait()
	s.closeClients()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

// pump copies the command's output to the emulator and to every attached
// terminal, and returns when the command is done writing.
func (s *server) pump() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			s.mu.Lock()
			_, _ = s.screen.Write(chunk)
			clients := make([]*client, 0, len(s.clients))
			for c := range s.clients {
				clients = append(clients, c)
			}
			s.mu.Unlock()
			for _, c := range clients {
				if err := c.send(kindOutput, chunk); err != nil {
					s.drop(c)
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *server) accept(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go s.serve(conn)
	}
}

// serve handles one attached terminal for as long as it stays.
func (s *server) serve(conn net.Conn) {
	defer conn.Close()

	k, payload, err := readFrame(conn)
	if err != nil {
		return
	}
	if k == kindStop {
		// End the session: signal the command's group, so anything it started
		// goes with it, and let the keeper exit when the pty closes.
		if s.cmd.Process != nil {
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGHUP)
			_ = syscall.Kill(s.cmd.Process.Pid, syscall.SIGHUP)
		}
		_ = writeFrame(conn, kindStatus, []byte("stopping"))
		return
	}
	if k == kindAsk {
		// A status request: say what is running, then hang up. This is how
		// `ush -s` finds out whether the session it is about to share is
		// already busy with something else.
		_ = writeFrame(conn, kindStatus, []byte(strings.Join(s.command, " ")))
		return
	}
	if k != kindResize {
		return // an attach opens with its window size
	}
	win, err := decodeSize(payload)
	if err != nil {
		return
	}
	// The payload's high bit of the width would be a size no terminal has;
	// read-only is its own frame instead, sent immediately after.
	c := &client{conn: conn}
	if k2, ro, err := readFrame(conn); err == nil && k2 == kindInput && len(ro) == 1 {
		c.readOnly = ro[0] == 1
	}

	s.resize(win)

	// Repaint: the emulator knows what is on screen, so a terminal attaching
	// midway sees the session as it is rather than waiting for the program to
	// write something.
	//
	// Rendering, sending and joining the fan-out all happen under one hold of
	// the lock. Split them and a write that lands in between is in neither the
	// repaint nor the stream: the new terminal would sit there missing exactly
	// the output that arrived as it attached.
	s.mu.Lock()
	err = c.send(kindOutput, []byte("\x1b[2J\x1b[H"+s.screen.Render()+modeSequences(s.modes)))
	if err == nil {
		s.clients[c] = struct{}{}
	}
	s.mu.Unlock()
	if err != nil {
		return
	}
	defer s.drop(c)

	for {
		k, payload, err := readFrame(conn)
		if err != nil {
			return
		}
		switch k {
		case kindInput:
			if c.readOnly {
				continue
			}
			if _, err := s.ptmx.Write(payload); err != nil {
				return
			}
		case kindResize:
			if win, err := decodeSize(payload); err == nil {
				// Last resize wins, which is what a person expects when they
				// are the one who just resized.
				s.resize(win)
			}
		}
	}
}

func (s *server) resize(win Size) {
	if win.Width <= 0 || win.Height <= 0 {
		return
	}
	s.mu.Lock()
	s.screen.Resize(win.Width, win.Height)
	s.mu.Unlock()
	_ = pty.Setsize(s.ptmx, &pty.Winsize{Cols: uint16(win.Width), Rows: uint16(win.Height)})
}

func (s *server) drop(c *client) {
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
	_ = c.conn.Close()
}

func (s *server) closeClients() {
	s.mu.Lock()
	clients := make([]*client, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.clients = map[*client]struct{}{}
	s.mu.Unlock()
	for _, c := range clients {
		_ = c.conn.Close()
	}
}

// Exists reports whether a keeper is listening for this name, and clears a
// socket left behind by one that is not.
func Exists(name string) bool {
	sock, err := SocketPath(name)
	if err != nil {
		return false
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			_ = os.Remove(sock)
		}
		return false
	}
	conn.Close()
	return true
}

// Command asks a keeper what it is running.
func Command(name string) string {
	sock, err := SocketPath(name)
	if err != nil {
		return ""
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return ""
	}
	defer conn.Close()
	if err := writeFrame(conn, kindAsk, nil); err != nil {
		return ""
	}
	k, payload, err := readFrame(conn)
	if err != nil || k != kindStatus {
		return ""
	}
	return string(payload)
}

// ErrNoSession is Stop's answer when nothing by that name is kept.
var ErrNoSession = errors.New("no such kept session")

// Stop ends a kept session: the command is signalled, and the keeper exits
// with it. A command that ignores the hangup is still running, and still
// shared, when Stop gives up waiting, and that is an error.
func Stop(name string) error {
	sock, err := SocketPath(name)
	if err != nil {
		return ErrNoSession
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return ErrNoSession
	}
	defer conn.Close()
	if err := writeFrame(conn, kindStop, nil); err != nil {
		return ErrNoSession
	}
	_, _, _ = readFrame(conn)
	for i := 0; i < 50; i++ {
		if !Exists(name) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("%q is still running: its command ignored the hangup", name)
}

// List names the kept sessions that still answer.
func List() []string {
	dir, err := Dir()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".sock")
		if name == e.Name() {
			continue
		}
		if Exists(name) {
			names = append(names, name)
		}
	}
	return names
}

var _ io.Writer = (*vt.Emulator)(nil)
