// Package rendezvous is everything that runs on the rendezvous host: the
// daemon, the session registry, the menu, and the terminal that joins one
// shared session.
package rendezvous

import (
	"fmt"
	"io"
	"net"
	"os"

	"crypto/subtle"

	"golang.org/x/crypto/ssh"
)

// Exit codes, which the menu and the client both read.
const (
	ExitEnded    = 0  // the session closed from the far side
	ExitNoJoin   = 2  // never attached
	ExitDetached = 10 // the user chose "back to list" in the Ctrl-] panel
	ExitQuit     = 11 // the user chose "disconnect" in the Ctrl-] panel
)

// Options say how the session child reaches the daemon and which session.
type Options struct {
	Attach  string // the daemon's unix socket
	User    string // the login user the daemon accepts
	Session string // the session to join; empty for the menu
	Key     string // the shared key
	HostKey string // the daemon's host key, as an authorized_keys line
	Dir     string // the session registry
	Viewers string // where joined viewers are marked
}

// RestoreTerm puts back the TERM that was stashed before bubbletea's package
// init ran. That init asks the terminal for its background colour and reads
// while it waits, which would swallow keystrokes meant for the session and
// stall on terminals that never answer; termenv skips the query when TERM is
// "dumb", so whatever starts a session sets TERM=dumb and the real value in
// USH_TERM, and this puts it back before anything uses it.
func RestoreTerm() {
	if t, ok := os.LookupEnv("USH_TERM"); ok {
		os.Setenv("TERM", t)
		os.Unsetenv("USH_TERM")
	}
}

// Menu shows the live session list and joins what the user picks.
func Menu(o Options) int {
	return runMenu(menuConfig{
		dir:     o.Dir,
		viewers: o.Viewers,
		join: func(s session) target {
			return target{attach: o.Attach, user: o.User, session: s.ID, key: o.Key,
				hostKey: o.HostKey, registryDir: o.Dir, viewersDir: o.Viewers}
		},
	})
}

// Join attaches to one session and returns the exit code that says how it
// ended, which is how the menu tells apart a session closing from a user
// detaching.
func Join(o Options, in *os.File, out io.Writer) int {
	result, err := joinSession(target{attach: o.Attach, user: o.User, session: o.Session, key: o.Key,
		hostKey: o.HostKey, registryDir: o.Dir, viewersDir: o.Viewers}, in, out)
	switch result {
	case detached:
		return ExitDetached
	case disconnected:
		return ExitQuit
	case failed:
		fmt.Fprintf(os.Stderr, "ush: %v\n", err)
		return ExitNoJoin
	default:
		return ExitEnded
	}
}

// pinnedHostKey accepts exactly one host key, given as an authorized_keys
// line: the daemon's own, which the child is handed at start. Trust on first
// use would only add a window in which something else could be trusted.
func pinnedHostKey(line string) (ssh.HostKeyCallback, error) {
	want, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, fmt.Errorf("pinned host key: %w", err)
	}
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if subtle.ConstantTimeCompare(key.Marshal(), want.Marshal()) == 1 {
			return nil
		}
		return fmt.Errorf("the daemon offered a host key that is not its own: %s", ssh.FingerprintSHA256(key))
	}, nil
}
