package keep

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"ush/internal/detach"
	"ush/internal/register"
)

// Attach connects this terminal to a kept session until the session ends or
// the user detaches. It reports whether the session is still running. The
// detach key asks first: anything but yes reattaches, and the keeper repaints.
func Attach(name string, readOnly bool, in *os.File, out io.Writer) (detached bool, err error) {
	sock, err := SocketPath(name)
	if err != nil {
		return false, err
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return false, fmt.Errorf("keep: no session named %q", name)
	}

	fd := int(in.Fd())
	if state, err := term.MakeRaw(fd); err == nil {
		defer func() { _ = term.Restore(fd, state) }()
	}
	// The program may still be running with the mouse on; this terminal is
	// going back to a shell. The keeper turns the modes back on for the next
	// attach.
	defer func() { _, _ = io.WriteString(out, detach.ResetModes) }()

	for {
		pressed, err := attachConn(conn, readOnly, in, out)
		if err != nil || !pressed {
			return false, err
		}
		if confirmDetach(name, labelSocket(name, readOnly), in, out) {
			return true, nil
		}
		if conn, err = net.Dial("unix", sock); err != nil {
			return false, nil // it ended while the question was up
		}
	}
}

// attachConn relays this terminal over conn until the session ends or the
// detach key is pressed, and reports which. conn is closed on return, so no
// more output lands after it.
func attachConn(conn net.Conn, readOnly bool, in *os.File, out io.Writer) (pressed bool, err error) {
	defer conn.Close()

	fd := int(in.Fd())
	win := Size{Width: 80, Height: 24}
	if w, h, err := term.GetSize(fd); err == nil {
		win = Size{Width: w, Height: h}
	}
	if err := writeFrame(conn, kindResize, win.encode()); err != nil {
		return false, err
	}
	flag := byte(0)
	if readOnly {
		flag = 1
	}
	if err := writeFrame(conn, kindInput, []byte{flag}); err != nil {
		return false, err
	}

	// Resizes go straight through: the keeper resizes the pty and its screen,
	// so whoever resized last is the size the program sees.
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer func() {
		signal.Stop(winch)
		close(winch)
	}()
	go func() {
		for range winch {
			if w, h, err := term.GetSize(fd); err == nil {
				_ = writeFrame(conn, kindResize, Size{Width: w, Height: h}.encode())
			}
		}
	}()

	left := make(chan struct{})
	go func() {
		defer close(left)
		// The kept program may turn on the Kitty keyboard protocol, and then
		// a terminal that implements it reports Ctrl-] as an escape sequence
		// rather than a byte, so both spellings have to be recognised.
		var scan detach.Scanner
		buf := make([]byte, 4096)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				forward, pressed := scan.Scan(buf[:n])
				if len(forward) > 0 {
					if err := writeFrame(conn, kindInput, forward); err != nil {
						return
					}
				}
				if pressed {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	ended := make(chan error, 1)
	go func() {
		for {
			k, payload, err := readFrame(conn)
			if err != nil {
				ended <- err
				return
			}
			if k == kindOutput {
				if _, err := out.Write(payload); err != nil {
					ended <- err
					return
				}
			}
		}
	}()

	select {
	case <-left:
		return true, nil
	case <-ended:
		return false, nil // the session went away, which is not this side's error
	}
}

// labelSocket is where the process sharing name answers for its label, or ""
// when this terminal may not change it.
func labelSocket(name string, readOnly bool) string {
	dir, err := Dir()
	if err != nil || readOnly {
		return ""
	}
	return register.ControlPath(dir, name)
}

// quietModes turns off the reports a program may have on that would arrive
// here as keystrokes: mouse, focus and bracketed paste. A reattach turns them
// back on.
const quietModes = "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1004l\x1b[?2004l"

// confirmDetach asks whether to leave name, and reads one key: d, y or the
// detach key again mean yes. A shared session's screen also offers n and e,
// to relabel it in the menu through ctl, its share's control socket. The
// screen is cleared either way; a reattach repaints it. Plain keys are pushed
// so the answer arrives as a byte even if the program had the Kitty protocol
// on.
func confirmDetach(name, ctl string, in *os.File, out io.Writer) bool {
	_, _ = io.WriteString(out, quietModes+detach.PushPlainKeys)
	defer func() { _, _ = io.WriteString(out, detach.PopKeys+"\x1b[2J\x1b[H\x1b[?25h") }()
	status := ""
	buf := make([]byte, 64)
	for {
		label, desc, err := "", "", errors.New("not shared")
		if ctl != "" {
			label, desc, err = register.GetLabel(ctl)
		}
		labelled, described := err == nil, desc != ""
		_, _ = fmt.Fprintf(out, "\x1b[?25l\x1b[2J\x1b[H\r\n"+
			"  Detach from %q?\r\n\r\n"+
			"  It keeps running and stays shared; ush attach %s gets back to it.\r\n\r\n",
			name, name)
		if labelled {
			shown := desc
			if !described {
				shown = "(none: the menu shows the command)"
			}
			_, _ = fmt.Fprintf(out, "  listed as     %s\r\n  description   %s\r\n\r\n"+
				"  d  detach    n  rename    e  description    any other key  back\r\n", label, shown)
		} else {
			_, _ = io.WriteString(out, "  d  detach        any other key  back\r\n")
		}
		if status != "" {
			_, _ = fmt.Fprintf(out, "\r\n  %s\r\n", status)
		}
		n, err := in.Read(buf)
		if err != nil || n == 0 {
			return true
		}
		switch k := buf[0]; {
		case k == 'd' || k == 'D' || k == 'y' || k == 'Y' || k == detach.Key:
			return true
		case labelled && (k == 'n' || k == 'N'):
			status = relabel(ctl, in, out, "name", label)
		case labelled && (k == 'e' || k == 'E'):
			status = relabel(ctl, in, out, "description", desc)
		default:
			return false
		}
	}
}

// relabel edits one field of the label and saves it through ctl, and says
// how that went.
func relabel(ctl string, in io.Reader, out io.Writer, field, value string) string {
	limit := register.MaxDescription
	if field == "name" {
		limit = 32 // what register.ValidName allows
	}
	value, ok := readLine(in, out, field, value, limit)
	if !ok {
		return ""
	}
	var err error
	if field == "name" {
		_, _, err = register.SetLabel(ctl, &value, nil)
	} else {
		_, _, err = register.SetLabel(ctl, nil, &value)
	}
	switch {
	case err != nil:
		return "not saved: " + err.Error()
	case field == "name":
		return "renamed; the menu gets it now, or on reconnect (ush attach keeps the old name)"
	default:
		return "saved; the menu gets it now, or on reconnect"
	}
}

// readLine edits one line of plain ASCII, up to limit bytes, on the raw
// terminal: Enter keeps it, Esc or Ctrl-C drops it, Backspace and Ctrl-U
// delete.
func readLine(in io.Reader, out io.Writer, field, value string, limit int) (string, bool) {
	line := []byte(value)
	buf := make([]byte, 64)
	for {
		_, _ = fmt.Fprintf(out, "\x1b[2J\x1b[H\r\n  New %s (Enter saves, Esc cancels)\r\n\r\n  %s: %s\x1b[?25h",
			field, field, line)
		n, err := in.Read(buf)
		if err != nil || n == 0 {
			return "", false
		}
		for _, b := range buf[:n] {
			switch {
			case b == '\r' || b == '\n':
				return string(line), true
			case b == 0x1b || b == 0x03:
				return "", false
			case b == 0x7f || b == 0x08:
				if len(line) > 0 {
					line = line[:len(line)-1]
				}
			case b == 0x15:
				line = line[:0]
			case b >= 0x20 && b < 0x7f && len(line) < limit:
				line = append(line, b)
			}
		}
	}
}
