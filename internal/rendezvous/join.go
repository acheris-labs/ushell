package rendezvous

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/cancelreader"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"

	"ush/internal/detach"
	"ush/internal/tunnel"
)

// target is how to reach one session: the daemon's socket and which session,
// plus where the registry and viewer marks live (for the Ctrl-] panel).
type target struct {
	attach, user, session, key, hostKey string
	registryDir, viewersDir             string
}

type outcome int

const (
	ended        outcome = iota // the session closed from the far side
	detached                    // back to the list, from the Ctrl-] panel
	failed                      // never attached
	disconnected                // quit ush entirely, from the Ctrl-] panel
)

// replayLimit caps the output buffered while the Ctrl-] panel is up; past it
// the session is asked to redraw instead.
const replayLimit = 4 << 20

// The session paints on the alternate screen, so leaving it puts back whatever
// the terminal was showing before ush ran instead of leaving the shared
// program's last frame behind. Leaving also clears the modes a remote program
// may have had on when we left (detach.ResetModes).
const (
	enterAltScreen = "\x1b[?1049h"
	leaveAltScreen = detach.ResetModes + "\x1b[?1049l"
)

// While joined, the terminal's title names the session. The title it had is
// saved on xterm's title stack on the way in and put back on the way out.
const (
	pushTitle = "\x1b[22;0t"
	popTitle  = "\x1b[23;0t"
)

// sessionTitle sets the title to name@machine, or is empty when the registry
// doesn't have the session. Registry fields are printable ASCII already, so
// nothing in them can end the escape early.
func sessionTitle(registryDir, id string) string {
	s, ok := readSession(registryDir, id)
	if !ok {
		return ""
	}
	return "\x1b]2;" + s.Name + "@" + s.Machine + "\x07"
}

// joinSession connects tty to one session until it ends, or until the user
// leaves through the Ctrl-] panel. For its duration it owns the terminal: raw
// mode, resize forwarding, and every keystroke. Whenever it stops reading tty
// (for the panel, or to return) the reader is cancelled and waited for, so
// the panel, or the menu that called it, gets the next key.
func joinSession(t target, tty *os.File, out io.Writer) (outcome, error) {
	output := newPausable(out, replayLimit)
	v, closeAll, err := attach(t, tty)
	if err != nil {
		return failed, err
	}
	defer closeAll()

	id := t.session
	joined := time.Now()
	if t.viewersDir != "" {
		if leave, err := markViewer(t.viewersDir, id); err == nil {
			defer leave()
		}
	}

	fd := int(tty.Fd())
	if state, err := term.MakeRaw(fd); err == nil {
		defer term.Restore(fd, state)
	}

	fmt.Fprint(out, enterAltScreen)
	defer fmt.Fprint(out, leaveAltScreen)
	fmt.Fprint(out, pushTitle+sessionTitle(t.registryDir, id))
	defer fmt.Fprint(out, popTitle)

	resize := make(chan os.Signal, 1)
	signal.Notify(resize, syscall.SIGWINCH)
	defer signal.Stop(resize)
	stopResize := make(chan struct{})
	defer close(stopResize)
	redraw := func() {
		if w, h, err := term.GetSize(fd); err == nil {
			_ = v.Resize(tunnel.Size{Cols: uint32(w), Rows: uint32(h)})
		}
	}
	go func() {
		for {
			select {
			case <-resize:
				redraw()
			case <-stopResize:
				return
			}
		}
	}()

	// The session's output is copied here; when the copy ends, the session
	// has ended (the sharer closed the channel) or the link is gone.
	end := make(chan struct{})
	go func() {
		_, _ = io.Copy(output, v)
		close(end)
	}()

	// Nudge the size so the program repaints: a viewer only receives what is
	// written after joining, and a TUI that paints deltas (herdr, and tmux
	// when sizes already match) would otherwise leave the screen blank.
	go func() {
		// Twice: the first nudge can land before the program is ready to
		// repaint (herdr, just after it attaches), and a second one costs
		// nothing but one redraw.
		for _, delay := range []time.Duration{150 * time.Millisecond, 1200 * time.Millisecond} {
			time.Sleep(delay)
			nudge(v, fd)
		}
	}()

	for {
		pressed, err := forwardInput(tty, v, end)
		if err != nil {
			return failed, err
		}
		if !pressed {
			return ended, nil
		}

		output.pause()
		// The program may have put the terminal into the Kitty keyboard
		// protocol; push plain encoding so the panel sees ordinary keys, and
		// pop afterwards to give the program back whatever it had set.
		fmt.Fprint(out, detach.PushPlainKeys)
		choice := showPanel(t, id, joined, tty, out)
		fmt.Fprint(out, detach.PopKeys)
		select {
		case <-end: // it ended while the panel was up
			output.resume()
			return ended, nil
		default:
		}
		switch choice {
		case choiceList:
			output.resume()
			return detached, nil
		case choiceDisconnect:
			output.resume()
			return disconnected, nil
		}
		// The panel drew on the same alternate screen, which clears on entry,
		// so the session's screen is gone: come back to it, replay what was
		// buffered, and ask the program to repaint. The panel may have
		// renamed the session, so the title is set again too.
		fmt.Fprint(out, enterAltScreen+sessionTitle(t.registryDir, id))
		output.resume()
		nudge(v, fd)
	}
}

// forwardInput copies keystrokes to the session until the detach key (true)
// or the session ends (false). Its reader is cancelled and waited for before
// it returns, so nothing keeps reading tty afterwards.
func forwardInput(tty *os.File, stdin io.Writer, end <-chan struct{}) (bool, error) {
	in, err := cancelreader.NewReader(tty)
	if err != nil {
		return false, err
	}
	defer in.Close()

	pressed := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		var scan detach.Scanner
		for {
			n, err := in.Read(buf)
			if n > 0 {
				forward, detach := scan.Scan(buf[:n])
				if len(forward) > 0 {
					if _, err := stdin.Write(forward); err != nil {
						return
					}
				}
				if detach {
					close(pressed)
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	select {
	case <-pressed:
		<-done
		return true, nil
	case <-end:
		in.Cancel()
		<-done
		return false, nil
	case <-done: // the terminal went away
		return false, nil
	}
}

// showPanel runs the Ctrl-] panel on the real terminal (not the paused
// session output) and returns what the user chose.
func showPanel(t target, id string, joined time.Time, tty *os.File, out io.Writer) panelChoice {
	info := panelInfo{session: session{ID: id, Name: id}, viewers: 1, joined: joined}
	if s, ok := readSession(t.registryDir, id); ok {
		info.session = s
	}
	if t.viewersDir != "" {
		info.viewers = max(countViewers(t.viewersDir, id), 1)
	}
	var rename func(string) error
	if t.registryDir != "" {
		rename = func(name string) error { return renameSession(t.registryDir, id, name) }
	}
	p := newPanel(info, rename, time.Now)
	if _, err := tea.NewProgram(p, tea.WithAltScreen(), tea.WithInput(tty), tea.WithOutput(out)).Run(); err != nil {
		return choiceBack
	}
	return p.choice
}

// attach logs in to the daemon over its unix socket and opens the viewer
// channel for the session, asking for a terminal of this tty's size.
func attach(t target, tty *os.File) (*tunnel.Viewer, func(), error) {
	if t.session == "" {
		return nil, nil, errors.New("no session to join")
	}
	pem, err := os.ReadFile(t.key)
	if err != nil {
		return nil, nil, err
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing %s: %w", t.key, err)
	}
	hostKeys, err := pinnedHostKey(t.hostKey)
	if err != nil {
		return nil, nil, err
	}
	client, err := ssh.Dial("unix", t.attach, &ssh.ClientConfig{
		User:            t.user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKeys,
		Timeout:         10 * time.Second,
	})
	if err != nil {
		return nil, nil, err
	}

	w, h, err := term.GetSize(int(tty.Fd()))
	if err != nil {
		w, h = 80, 24
	}
	termType := os.Getenv("TERM")
	if termType == "" {
		termType = "xterm-256color"
	}
	v, err := tunnel.Dial(client, t.session, termType, tunnel.Size{Cols: uint32(w), Rows: uint32(h)})
	if err != nil {
		client.Close()
		return nil, nil, err
	}
	return v, func() { v.Close(); client.Close() }, nil
}

// nudge makes the program repaint by changing the window size and changing it
// back; there is no escape sequence that asks a terminal program to redraw.
// Ctrl-] r reaches this too, for when a screen still comes back blank.
func nudge(v *tunnel.Viewer, fd int) {
	w, h, err := term.GetSize(fd)
	if err != nil || h <= 1 {
		return
	}
	_ = v.Resize(tunnel.Size{Cols: uint32(w), Rows: uint32(h - 1)})
	time.Sleep(120 * time.Millisecond)
	_ = v.Resize(tunnel.Size{Cols: uint32(w), Rows: uint32(h)})
}
