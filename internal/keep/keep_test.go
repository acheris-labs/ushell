package keep

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ush/internal/detach"
)

// withHome points the socket directory at a temporary one, so a test never
// touches a real session. It has to be a short path: a unix socket address is
// about 100 bytes, and Go's own temp directories are longer than that.
func withHome(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ushk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("USH_KEEP_DIR", dir)
}

// start runs a keeper in the background and waits for it to answer.
func start(t *testing.T, name string, command ...string) chan int {
	t.Helper()
	done := make(chan int, 1)
	go func() {
		code, err := Run(name, command, nil, os.Environ(), Size{Width: 80, Height: 24})
		if err != nil {
			t.Errorf("keeper: %v", err)
		}
		done <- code
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if Exists(name) {
			return done
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("keeper %q never came up", name)
	return nil
}

// attach speaks the protocol directly, so a test needs no terminal.
type conn struct {
	net.Conn
	t *testing.T
}

func attach(t *testing.T, name string, readOnly bool) *conn {
	t.Helper()
	sock, err := SocketPath(name)
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := writeFrame(c, kindResize, Size{Width: 80, Height: 24}.encode()); err != nil {
		t.Fatal(err)
	}
	flag := byte(0)
	if readOnly {
		flag = 1
	}
	if err := writeFrame(c, kindInput, []byte{flag}); err != nil {
		t.Fatal(err)
	}
	return &conn{Conn: c, t: t}
}

// waitFor reads output frames until one carries want, or time runs out.
func (c *conn) waitFor(want string, within time.Duration) string {
	c.t.Helper()
	var seen strings.Builder
	_ = c.SetReadDeadline(time.Now().Add(within))
	for {
		k, payload, err := readFrame(c)
		if err != nil {
			return seen.String()
		}
		if k != kindOutput {
			continue
		}
		seen.Write(payload)
		if strings.Contains(seen.String(), want) {
			return seen.String()
		}
	}
}

func (c *conn) typ(s string) {
	c.t.Helper()
	if err := writeFrame(c, kindInput, []byte(s)); err != nil {
		c.t.Fatal(err)
	}
}

// The whole point: a terminal that attaches after the fact sees what is on
// screen, rather than a blank window until the program writes again.
func TestAttachingRepaintsTheScreen(t *testing.T) {
	withHome(t)
	start(t, "paint", "sh", "-c", "printf 'MARKER-BEFORE-ATTACH\\r\\n'; exec cat")
	time.Sleep(500 * time.Millisecond) // let the marker reach the screen

	c := attach(t, "paint", false)
	if got := c.waitFor("MARKER-BEFORE-ATTACH", 5*time.Second); !strings.Contains(got, "MARKER-BEFORE-ATTACH") {
		t.Fatalf("attach did not repaint the screen; got %q", got)
	}
}

func TestTypingReachesTheCommand(t *testing.T) {
	withHome(t)
	start(t, "typing", "cat")
	c := attach(t, "typing", false)
	c.typ("hello-there\r")
	if got := c.waitFor("hello-there", 5*time.Second); !strings.Contains(got, "hello-there") {
		t.Fatalf("input never came back; got %q", got)
	}
}

func TestReadOnlyTerminalsWatchWithoutTyping(t *testing.T) {
	withHome(t)
	start(t, "watch", "cat")
	writer := attach(t, "watch", false)
	watcher := attach(t, "watch", true)

	watcher.typ("SHOULD-NOT-APPEAR\r")
	writer.typ("FROM-THE-WRITER\r")

	got := watcher.waitFor("FROM-THE-WRITER", 5*time.Second)
	if !strings.Contains(got, "FROM-THE-WRITER") {
		t.Fatalf("a read-only terminal should still see output; got %q", got)
	}
	if strings.Contains(got, "SHOULD-NOT-APPEAR") {
		t.Fatal("a read-only terminal typed into the session")
	}
}

// Two terminals on one session both see everything: that is what makes a
// viewer's screen live rather than frozen.
func TestEveryTerminalSeesTheOutput(t *testing.T) {
	withHome(t)
	start(t, "both", "cat")
	first := attach(t, "both", false)
	second := attach(t, "both", false)

	first.typ("SEEN-BY-BOTH\r")
	for i, c := range []*conn{first, second} {
		if got := c.waitFor("SEEN-BY-BOTH", 5*time.Second); !strings.Contains(got, "SEEN-BY-BOTH") {
			t.Fatalf("terminal %d missed the output; got %q", i+1, got)
		}
	}
}

func TestStatusSaysWhatIsRunning(t *testing.T) {
	withHome(t)
	start(t, "status", "sh", "-c", "exec cat")
	if got := Command("status"); got != "sh -c exec cat" {
		t.Fatalf("status = %q, want the command", got)
	}
	if names := List(); len(names) != 1 || names[0] != "status" {
		t.Fatalf("list = %v, want [status]", names)
	}
}

// The session is the command: when it exits, the keeper goes with it and
// leaves nothing behind for the next one to trip over.
func TestTheKeeperDiesWithItsCommand(t *testing.T) {
	withHome(t)
	// Run directly rather than through start(): this command is gone before
	// anything could observe the session alive, which is the point.
	code, err := Run("short", []string{"sh", "-c", "exit 7"}, nil, os.Environ(), Size{Width: 80, Height: 24})
	if err != nil {
		t.Fatal(err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}

	sock, _ := SocketPath("short")
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("socket %s was left behind", filepath.Base(sock))
	}
	if Exists("short") {
		t.Error("a finished session should not be listed")
	}
}

func TestNamesAreChecked(t *testing.T) {
	withHome(t)
	if _, err := Run("../escape", []string{"true"}, nil, nil, Size{}); err == nil {
		t.Fatal("a name that is a path must be refused")
	}
	if _, err := Run("-read-only", []string{"true"}, nil, nil, Size{}); err == nil {
		t.Fatal("a name that reads as a flag must be refused")
	}
	if _, err := Run("fine", nil, nil, nil, Size{}); err == nil {
		t.Fatal("a keeper with nothing to run must be refused")
	}
}

func TestOneKeeperPerName(t *testing.T) {
	withHome(t)
	start(t, "taken", "cat")
	if _, err := Run("taken", []string{"cat"}, nil, os.Environ(), Size{Width: 80, Height: 24}); err == nil {
		t.Fatal("a second keeper for the same name must be refused")
	}
}

// A terminal attaching midway gets back the modes the program turned on:
// the last one to detach turned them all off.
func TestAttachingTurnsTheProgramsModesBackOn(t *testing.T) {
	withHome(t)
	start(t, "modes", "sh", "-c", `printf '\033[?1000h\033[?1002h\033[?1006h\033[?1000l\033[?25l'; exec cat`)
	time.Sleep(500 * time.Millisecond) // let the modes reach the emulator

	got := attach(t, "modes", false).waitFor("\x1b[?25l", 5*time.Second)
	for _, want := range []string{"\x1b[?1002h", "\x1b[?1006h", "\x1b[?1000l", "\x1b[?25l"} {
		if !strings.Contains(got, want) {
			t.Errorf("repaint lacks %q; got %q", want, got)
		}
	}
	if strings.Contains(got, "\x1b[?2004") {
		t.Errorf("repaint touched bracketed paste, which the program never did; got %q", got)
	}
}

// lockedBuffer is written by Attach's output goroutine and its cleanup.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Whatever the program left on, the terminal goes back to its shell without it.
func TestLeavingTurnsTheModesOff(t *testing.T) {
	withHome(t)
	start(t, "leave", "sh", "-c", `printf '\033[?1003h\033[?1006h'; sleep 0.5`)
	in, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	defer w.Close()
	var out lockedBuffer
	if _, err := Attach("leave", false, in, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.HasSuffix(got, detach.ResetModes) {
		t.Fatalf("output does not end with the reset; got %q", got)
	}
}

func TestStopEndsTheSession(t *testing.T) {
	withHome(t)
	start(t, "stopme", "cat")
	if err := Stop("stopme"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if Exists("stopme") {
		t.Fatal("still there after stop")
	}
}

// A command that ignores the hangup outlives stop, and stop must say so
// rather than report it stopped while it is still kept and shared.
func TestStopSaysWhenTheCommandStaysUp(t *testing.T) {
	withHome(t)
	start(t, "stubborn", "sh", "-c", `trap "" HUP; sleep 8`)
	err := Stop("stubborn")
	if err == nil || errors.Is(err, ErrNoSession) {
		t.Fatalf("got %v, want an error saying it is still running", err)
	}
}

func TestStopWithNothingThere(t *testing.T) {
	withHome(t)
	if err := Stop("absent"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("got %v, want ErrNoSession", err)
	}
}

// Anyone who can reach the socket directory can attach to every kept shell,
// so one set by USH_KEEP_DIR must be private to this user, not just exist.
func TestKeepDirMustBePrivate(t *testing.T) {
	open, err := os.MkdirTemp("/tmp", "ushk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(open) })
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("USH_KEEP_DIR", open)
	if _, err := Dir(); err == nil {
		t.Error("a 0755 directory should be refused")
	}

	private, err := os.MkdirTemp("/tmp", "ushk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(private) })
	link := private + "-link"
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	t.Setenv("USH_KEEP_DIR", link)
	if _, err := Dir(); err == nil {
		t.Error("a symlink should be refused")
	}

	t.Setenv("USH_KEEP_DIR", private)
	if dir, err := Dir(); err != nil || dir != private {
		t.Errorf("a private directory: got %q, %v", dir, err)
	}
	fresh := filepath.Join(private, "new")
	t.Setenv("USH_KEEP_DIR", fresh)
	if _, err := Dir(); err != nil {
		t.Errorf("a directory that does not exist yet is created: %v", err)
	}
}
