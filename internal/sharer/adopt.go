//go:build darwin

package sharer

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"ush/internal/tunnel"
)

// adopt moves an already running process onto a new pty: lldb attaches and
// has it open the pty and dup2 it over stdin, stdout and stderr. The old
// terminal stays its controlling terminal, so job control and the signals a
// tty sends (^C, SIGWINCH, hangup) still come from there, not the new pty.
func adopt(pid int, size tunnel.Size, sized bool) (*os.File, error) {
	old, err := ttyOf(pid)
	if err != nil {
		return nil, err
	}
	ptmx, tty, err := pty.Open()
	if err != nil {
		return nil, err
	}
	defer tty.Close()
	// Whatever mode the process put its terminal in (raw, no echo) carries
	// over; a fresh pty would be cooked and echo every keystroke.
	if err := copyTermios(old, tty); err != nil {
		_ = ptmx.Close()
		return nil, err
	}
	if sized {
		_ = pty.Setsize(ptmx, winsize(size))
	}
	expr := fmt.Sprintf(`int $fd = (int)open("%s", 2); (int)dup2($fd, 0); (int)dup2($fd, 1); `+
		`(int)dup2($fd, 2); (int)close($fd); $fd`, tty.Name())
	out, err := exec.Command("lldb", "--batch", "-p", strconv.Itoa(pid),
		"-o", "expr -- "+expr, "-o", "detach").CombinedOutput()
	if bytes.Contains(out, []byte("Not allowed to attach")) {
		_ = ptmx.Close()
		return nil, refusal(pid)
	}
	if err != nil || !bytes.Contains(out, []byte("detached")) {
		_ = ptmx.Close()
		return nil, fmt.Errorf("lldb could not adopt %d: %v\n%s", pid, err, lastLines(out, 8))
	}
	if err := unix.Kill(pid, unix.SIGWINCH); err != nil {
		_ = ptmx.Close()
		return nil, err
	}
	return ptmx, nil
}

// ttyOf is the terminal pid's stdin is on.
func ttyOf(pid int) (string, error) {
	out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "0", "-Fn").Output()
	if err != nil {
		return "", fmt.Errorf("finding %d's terminal: %w", pid, err)
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if name, ok := strings.CutPrefix(sc.Text(), "n"); ok && strings.HasPrefix(name, "/dev/tty") {
			return name, nil
		}
	}
	return "", fmt.Errorf("%d's stdin is not a terminal", pid)
}

func copyTermios(from string, to *os.File) error {
	f, err := os.OpenFile(from, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	t, err := unix.IoctlGetTermios(int(f.Fd()), unix.TIOCGETA)
	if err != nil {
		return err
	}
	return unix.IoctlSetTermios(int(to.Fd()), unix.TIOCSETA, t)
}

// waitPid blocks until pid, which is not this process's child, exits.
func waitPid(pid int) error {
	kq, err := unix.Kqueue()
	if err != nil {
		return err
	}
	defer unix.Close(kq)
	ev := []unix.Kevent_t{{Ident: uint64(pid), Filter: unix.EVFILT_PROC,
		Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}}
	got := make([]unix.Kevent_t, 1)
	for {
		_, err := unix.Kevent(kq, ev, got, nil)
		if err == unix.ESRCH {
			return nil // already gone
		}
		if err != unix.EINTR {
			return err
		}
		ev = nil
	}
}

// hardened finds the runtime flag in codesign -dv's flags=0x10000(runtime),
// which may share the parentheses with others: (adhoc,runtime).
var hardened = regexp.MustCompile(`flags=0x[0-9a-f]+\([^)]*\bruntime\b`)

// refusal explains why macOS kept lldb out of pid, from how its binary is
// signed, and what to do instead.
func refusal(pid int) error {
	path := executableOf(pid)
	why := "macOS protects it from debuggers."
	sig, _ := exec.Command("codesign", "-dv", path).CombinedOutput()
	switch {
	case bytes.Contains(sig, []byte("Identifier=com.apple.")):
		why = "it is one of Apple's own programs, which System Integrity Protection guards."
	case hardened.Match(sig):
		why = "it is built with the hardened runtime, which refuses debuggers."
	}
	name := filepath.Base(path)
	return fmt.Errorf("can't adopt %d (%s): macOS won't let ush attach to it.\n"+
		"     Why: %s\n"+
		"     Instead: quit it and start it under ush, e.g. ush share -- %s",
		pid, path, why, name)
}

// executableOf is the file pid is running, or its name if that is unknown.
func executableOf(pid int) string {
	out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "txt", "-Fn").Output()
	if err == nil {
		sc := bufio.NewScanner(bytes.NewReader(out))
		for sc.Scan() {
			if name, ok := strings.CutPrefix(sc.Text(), "n"); ok {
				return name
			}
		}
	}
	out, _ = exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	return strings.TrimSpace(string(out))
}

func lastLines(b []byte, n int) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}
