package keep

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"ush/internal/detach"
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
		if confirmDetach(name, in, out) {
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

// confirmDetach asks whether to leave name, and reads one key: d, y or the
// detach key again mean yes. The screen is cleared either way; a reattach
// repaints it. Plain keys are pushed so the answer arrives as a byte even if
// the program had the Kitty protocol on.
func confirmDetach(name string, in *os.File, out io.Writer) bool {
	_, _ = fmt.Fprintf(out, "%s\x1b[?25l\x1b[2J\x1b[H\r\n"+
		"  Detach from %q?\r\n\r\n"+
		"  It keeps running and stays shared; ush attach %s gets back to it.\r\n\r\n"+
		"  d  detach        any other key  back\r\n",
		detach.PushPlainKeys, name, name)
	buf := make([]byte, 64)
	n, err := in.Read(buf)
	_, _ = io.WriteString(out, detach.PopKeys+"\x1b[2J\x1b[H\x1b[?25h")
	if err != nil || n == 0 {
		return true
	}
	switch buf[0] {
	case 'd', 'D', 'y', 'Y', detach.Key:
		return true
	}
	return false
}
