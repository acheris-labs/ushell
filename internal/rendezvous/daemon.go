package rendezvous

import (
	"bufio"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	gssh "charm.land/ssh"
	"golang.org/x/crypto/ssh"

	"ush/internal/tunnel"
)

// The daemon is the whole rendezvous: one public port, and everything the
// control side used to need sshd, a forced command and a shell script for.
//
//	share <blob> | list | join <sid> | menu     over a session channel
//	viewer@ush.acheris.net                       a channel: one viewer
//
// A share is held open for as long as it lasts, and every viewer of it is a
// channel the daemon opens back on that same connection. A session that
// needs a terminal runs on a real pty, as a child process: this binary
// re-executed with an undocumented subcommand. That keeps the menu and the
// join terminal on a genuine tty, which is what bubbletea, lipgloss and the
// detach key all expect, and it means a panic in one session cannot take
// the daemon down with it. The child reaches its viewer channel through the
// daemon's unix socket.
const (
	// SessionSubcommand is how the daemon re-executes itself for one session.
	// It is deliberately undocumented: it is an implementation detail of the
	// pty handoff, not something to run by hand.
	SessionSubcommand = "_session"

	// handshakeTimeout is the equivalent of OpenSSH's LoginGraceTime, which we
	// inherited for free before. It applies before authentication only: past
	// that a connection may idle forever, because an idle connection is
	// exactly what a registration and a quiet viewer both are.
	handshakeTimeout = 30 * time.Second

	// maxPending bounds connections that have not authenticated yet, standing
	// in for OpenSSH's MaxStartups. maxPendingPerIP keeps one address from
	// taking every slot: a real client authenticates at once, so it never
	// holds more than one for more than a moment.
	maxPending      = 128
	maxPendingPerIP = 8

	// logEvery rate-limits the log lines for refused and failed connections,
	// which arrive by the thousand from scanners or anyone filling the slots.
	logEvery = 10 * time.Second
)

// Daemon is a configured rendezvous. Everything it needs is derived from the
// seed by the caller, so it holds keys rather than paths to them.
type Daemon struct {
	Listen string // public address
	Attach string // unix socket the session child dials for its viewer channel

	Dir     string // the session registry
	Viewers string // where joined viewers are marked
	Key     string // the shared key, which the session child logs in with

	HostKey ed25519.PrivateKey // presented to clients; derived from the seed
	Login   ssh.PublicKey      // the only key that may log in
	User    string             // the only username that may log in

	Self   string      // this binary, re-executed for each session
	Logger *log.Logger // nil means the standard logger

	// EditPoll is how often a share's entry is checked for edits to pass to
	// its sharer; zero means every 2s. The panel that makes them is another
	// process, so the file is the only thing both can see.
	EditPoll time.Duration

	sharers     sharers
	closed      sync.Map // gssh.Session -> chan struct{}, closed with its channel
	hostKeyLine string   // HostKey's public half as an authorized_keys line, for the child to pin
}

func (d *Daemon) logf(format string, args ...any) {
	if d.Logger != nil {
		d.Logger.Printf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// Serve runs until the listener is closed.
func (d *Daemon) Serve(ln net.Listener) error {
	// Fail closed. The SSH library treats "no authentication handler" as "no
	// authentication required", so a daemon missing its keys must refuse to
	// listen rather than open an unauthenticated port.
	if len(d.HostKey) == 0 {
		return errors.New("ushd: no host key")
	}
	if d.Login == nil || d.User == "" {
		return errors.New("ushd: refusing to listen without a login key and user")
	}

	signer, err := ssh.NewSignerFromKey(d.HostKey)
	if err != nil {
		return err
	}
	d.hostKeyLine = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	failed := &every{period: logEvery}
	srv := &gssh.Server{
		Version:          "ushd",
		HostSigners:      []gssh.Signer{signer},
		PublicKeyHandler: d.allow,
		Handler:          d.handle,
		HandshakeTimeout: handshakeTimeout,
		ConnectionFailedCallback: func(c net.Conn, err error) {
			failed.logf(d.logf, "ushd: %v: %v", c.RemoteAddr(), err)
		},
		// The handler frees the connection's waiting slot, so it needs to
		// find the connection again.
		ConnCallback: func(ctx gssh.Context, c net.Conn) net.Conn {
			ctx.SetValue(slotKey{}, c)
			return c
		},
		// The viewer channel is ours; "session" is the library's, and
		// nothing else (no direct-tcpip) is served.
		ChannelHandlers: map[string]gssh.ChannelHandler{
			"session":          gssh.DefaultSessionHandler,
			tunnel.ChannelType: d.viewer,
		},
		// No IdleTimeout and no MaxTimeout on purpose: a registration never
		// sends anything after its first line, and a viewer watching a quiet
		// session sends nothing at all. Either would be disconnected.
	}
	if err := srv.SetOption(gssh.AllocatePty()); err != nil {
		return err
	}
	// The library releases a pty when its channel closes, but the child on
	// it carries on; and the session's context lasts as long as the whole
	// connection, which the app keeps for every session it has open. So the
	// release is also how runChild hears that its channel has gone.
	allocate := srv.PtyHandler
	srv.PtyHandler = func(ctx gssh.Context, sess gssh.Session, pty gssh.Pty) (func() error, error) {
		release, err := allocate(ctx, sess, pty)
		if err != nil {
			return nil, err
		}
		closed := make(chan struct{})
		d.closed.Store(sess, closed)
		return func() error {
			d.closed.Delete(sess)
			close(closed)
			return release()
		}, nil
	}
	// The library treats "no auth handler" as "no authentication required",
	// which would make this port an open SSH server. Never take that on trust.
	if srv.PublicKeyHandler == nil {
		return errors.New("ushd: refusing to listen without an authentication handler")
	}
	if d.Attach != "" {
		// The session child's way in. Stale from a previous run is normal.
		_ = os.Remove(d.Attach)
		un, err := net.Listen("unix", d.Attach)
		if err != nil {
			return err
		}
		defer un.Close()
		go func() {
			if err := srv.Serve(un); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, gssh.ErrServerClosed) {
				d.logf("ushd: attach socket: %v", err)
			}
		}()
	}
	return srv.Serve(newLimitListener(ln, d.logf))
}

// allow admits the one key derived from the seed, under the one username.
// It must stay free of side effects: the SSH library calls it for signature-
// less probes too, before any signature exists.
func (d *Daemon) allow(ctx gssh.Context, key gssh.PublicKey) bool {
	if ctx.User() != d.User {
		return false
	}
	return subtle.ConstantTimeCompare(key.Marshal(), d.Login.Marshal()) == 1
}

// handle dispatches one authenticated session. There is no shell here and no
// way to reach one: anything not listed is refused.
func (d *Daemon) handle(sess gssh.Session) {
	// Authenticated: this connection no longer counts against MaxStartups.
	if c, ok := sess.Context().Value(slotKey{}).(*slotConn); ok {
		c.free()
	}
	defer func() {
		// A panic in one session must not take the daemon, and with it every
		// registration, down. The library recovers the goroutines it starts;
		// this covers ours.
		if r := recover(); r != nil {
			d.logf("ushd: session panic: %v", r)
			_ = sess.Exit(1)
		}
	}()

	args := sess.Command()
	verb := "menu"
	if len(args) > 0 {
		verb = args[0]
	}
	switch verb {
	case "menu":
		d.runChild(sess, "-menu")
	case "share":
		d.share(sess, args[1:])
	case "register":
		// The upterm-era client. Its session would be listed but never
		// joinable, so say why instead.
		d.refuse(sess, "this ush is too old for this rendezvous: upgrade ush")
	case "list":
		_, _ = sess.Write(listJSON(d.Dir, d.Viewers))
		_ = sess.Exit(0)
	case "join":
		d.join(sess, args[1:])
	case "describe":
		d.describe(sess, args[1:])
	default:
		d.refuse(sess, fmt.Sprintf("unsupported command: %s", verb))
	}
}

// refuse reports a problem the way the shell script's die() did: on stderr,
// prefixed, exit 1. The client reads that stream to explain a registration it
// could not make, so it must not be merged into stdout.
func (d *Daemon) refuse(sess gssh.Session, msg string) {
	fmt.Fprintf(sess.Stderr(), "ush: %s\r\n", msg)
	_ = sess.Exit(1)
}

// share holds one entry for exactly as long as its connection. That is the
// whole liveness model: no heartbeats, no timers, nothing to expire. The
// connection is also what viewers ride: see viewer.
func (d *Daemon) share(sess gssh.Session, args []string) {
	if len(args) != 1 {
		d.refuse(sess, "share: expected a single payload argument")
		return
	}
	s, err := parseRegistration(args[0], time.Now())
	if err != nil {
		d.refuse(sess, err.Error())
		return
	}
	conn, ok := sess.Context().Value(gssh.ContextKeyConn).(*ssh.ServerConn)
	if !ok {
		d.refuse(sess, "share: no connection to open viewers on")
		return
	}
	if s.ID, err = newSessionID(); err != nil {
		d.refuse(sess, fmt.Sprintf("share: %v", err))
		return
	}
	// Viewers first, then the listing: a session the menu can see must be
	// joinable. The deferred removals run in the opposite order.
	d.sharers.add(s.ID, conn)
	defer d.sharers.remove(s.ID)
	if err := writeEntry(d.Dir, s); err != nil {
		d.refuse(sess, fmt.Sprintf("share: %v", err))
		return
	}
	defer removeEntry(d.Dir, s.ID)

	// The client waits for this line before it hands its terminal over.
	fmt.Fprintf(sess, "registered %s (%s)\n", s.Name, s.ID)
	d.logf("ushd: registered %s (%s) from %s", s.Name, s.ID, sess.RemoteAddr())

	// Block until the sharer goes away, whichever way it goes. Meanwhile
	// edits pass both ways: the sharer's arrive on stdin, and the ones made
	// here go back to it.
	known := &label{name: s.Name, description: s.Description}
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		lines := bufio.NewScanner(sess)
		for lines.Scan() {
			if name, desc, ok := known.take(d.Dir, s.ID, lines.Text()); ok {
				d.logf("ushd: %s (%s) relabelled by its sharer: %q, %q", s.Name, s.ID, name, desc)
			}
		}
		_, _ = io.Copy(io.Discard, sess) // a line too long ends the scanning, not the share
	}()
	// Not waited for: a sharer too old to read the updates could leave a
	// write blocked, and that must not hold up the deregistration.
	stop := make(chan struct{})
	defer close(stop)
	every := d.EditPoll
	if every == 0 {
		every = 2 * time.Second
	}
	go known.relay(sess, d.Dir, s.ID, every, stop)
	select {
	case <-gone:
	case <-sess.Context().Done():
	}
	known.end()
	d.logf("ushd: deregistered %s (%s)", s.Name, s.ID)
	_ = sess.Exit(0)
}

// label is the name and description a sharer was last sent. The sharer
// keeps them, and sends them again when it re-registers; without that, every
// reconnect would put back what it first registered.
type label struct {
	mu                sync.Mutex
	name, description string
	ended             bool // the share is gone: its entry must not be written again
}

// end stops take from writing the entry, which is about to be removed: a
// write landing after the removal would list a share that is not there.
func (l *label) end() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ended = true
}

// take applies an "update BASE64" line from the sharer: base64 of key=value
// lines, as a share's payload is, carrying only the fields it edited. A bad
// name is ignored and the description is cut down, as at registration. It
// reports whether the entry changed.
func (l *label) take(dir, id, line string) (name, description string, changed bool) {
	blob, ok := strings.CutPrefix(line, "update ")
	raw, err := base64.StdEncoding.DecodeString(blob)
	if !ok || err != nil {
		return "", "", false
	}
	l.mu.Lock() // so relay never sends the entry half written
	defer l.mu.Unlock()
	now, found := readSession(dir, id)
	if l.ended || !found {
		return "", "", false
	}
	name, description = now.Name, now.Description
	for _, kv := range strings.Split(string(raw), "\n") {
		key, value, _ := strings.Cut(kv, "=")
		switch key {
		case "name":
			if validName.MatchString(value) {
				name = value
			}
		case "description":
			description = clean(value, maxDescription)
		}
	}
	if name == now.Name && description == now.Description {
		return name, description, false
	}
	if renameSession(dir, id, name) != nil || describeSession(dir, id, description) != nil {
		return "", "", false
	}
	return name, description, true
}

// relay tells the sharer when its entry's name or description changes, as
// "update BASE64" lines in the same format. That includes the sharer's own
// edits, as they were saved: an edit from the menu can cross one from the
// sharer, and the last word has to be the entry's, or the two disagree.
func (l *label) relay(w io.Writer, dir, id string, every time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		l.mu.Lock()
		now, ok := readSession(dir, id)
		changed := ok && (now.Name != l.name || now.Description != l.description)
		if changed {
			l.name, l.description = now.Name, now.Description
		}
		l.mu.Unlock()
		if !changed {
			continue
		}
		blob := base64.StdEncoding.EncodeToString([]byte("name=" + now.Name + "\ndescription=" + now.Description + "\n"))
		if _, err := fmt.Fprintf(w, "update %s\n", blob); err != nil {
			return
		}
	}
}

// join attaches directly to one session, for scripting and tests. It checks
// the registry first: without that, a key holder could attach to a session
// that was never listed.
func (d *Daemon) join(sess gssh.Session, args []string) {
	id := ""
	if len(args) > 0 {
		id = args[0]
	}
	if !validSessionID.MatchString(id) {
		d.refuse(sess, "join: bad session id")
		return
	}
	if sessionGone(d.Dir, id) {
		d.refuse(sess, "join: no such session")
		return
	}
	d.runChild(sess, "-session", id)
}

// describe sets a session's description: describe ID [BASE64]. The text is
// base64, as a share's payload is, so it needs no quoting; leaving it out
// clears the description.
func (d *Daemon) describe(sess gssh.Session, args []string) {
	if len(args) < 1 || len(args) > 2 || !validSessionID.MatchString(args[0]) {
		d.refuse(sess, "describe: expected a session id and base64 text")
		return
	}
	var text []byte
	if len(args) == 2 {
		var err error
		if text, err = base64.StdEncoding.DecodeString(args[1]); err != nil {
			d.refuse(sess, "describe: text is not base64")
			return
		}
	}
	if sessionGone(d.Dir, args[0]) {
		d.refuse(sess, "describe: no such session")
		return
	}
	if err := describeSession(d.Dir, args[0], string(text)); err != nil {
		d.refuse(sess, fmt.Sprintf("describe: %v", err))
		return
	}
	_ = sess.Exit(0)
}

// runChild runs this binary again, on a real pty, for one session.
func (d *Daemon) runChild(sess gssh.Session, extra ...string) {
	ptyReq, _, ok := sess.Pty()
	if !ok {
		d.refuse(sess, "the menu needs a terminal: connect with ssh -t (or use: list, join <id>)")
		return
	}

	args := append([]string{SessionSubcommand,
		"-attach", d.Attach,
		"-user", d.User,
		"-key", d.Key,
		"-host-key", d.hostKeyLine,
		"-dir", d.Dir,
		"-viewers", d.Viewers,
	}, extra...)

	cmd := exec.Command(d.Self, args...)
	// Nothing of the daemon's environment goes through. TERM is dumb for the
	// child's startup, with the real value in USH_TERM: bubbletea's package
	// init asks the terminal for its background colour and reads while it
	// waits, which would swallow the first keystrokes.
	cmd.Env = []string{"TERM=dumb", "USH_TERM=" + ptyReq.Term, "HOME=/", "PATH=/usr/local/bin:/usr/bin:/bin"}

	if err := ptyReq.Start(cmd, gssh.WithJobControl()); err != nil {
		d.refuse(sess, fmt.Sprintf("starting the session: %v", err))
		return
	}
	// Hand the pty over: with our copy of the slave closed, the master sees
	// EOF when the child exits, which is what lets the library finish copying
	// the child's last bytes -- the alt-screen restore among them.
	_ = ptyReq.Slave.Close()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// Already gone if the channel closed before the child started.
	closed := make(chan struct{})
	if c, ok := d.closed.Load(sess); ok {
		closed = c.(chan struct{})
	} else {
		close(closed)
	}

	select {
	case err := <-done:
		_ = sess.Exit(exitCode(err))
	case <-closed:
		hangUp(cmd, done)
	case <-sess.Context().Done():
		hangUp(cmd, done)
	}
}

// hangUp ends a child whose viewer has gone, its channel or its whole
// connection. Nothing else does: without it a dropped viewer would leave a
// session, a pty and a viewer channel behind for good. WithJobControl put the
// child in its own session, so signal the group rather than the process.
func hangUp(cmd *exec.Cmd, done <-chan error) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGHUP)
	select {
	case <-done: // reaped
	case <-time.After(2 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
}

// exitCode passes the child's status through, so the menu keeps telling a
// session that ended apart from a user who detached.
func exitCode(err error) int {
	if err == nil {
		return ExitEnded
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if code := exit.ExitCode(); code >= 0 {
			return code
		}
		return ExitNoJoin // killed by a signal, which has no status of its own
	}
	return ExitNoJoin
}

// RunSession is the child half of the pty handoff: the daemon re-executes
// itself with SessionSubcommand and these arguments.
func RunSession(args []string) int {
	RestoreTerm()
	fs := flag.NewFlagSet("session", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var o Options
	fs.StringVar(&o.Attach, "attach", "/run/ush/attach.sock", "")
	fs.StringVar(&o.User, "user", "ush", "")
	fs.StringVar(&o.Session, "session", "", "")
	fs.StringVar(&o.Key, "key", "/run/ush/psk", "")
	fs.StringVar(&o.HostKey, "host-key", "", "")
	fs.StringVar(&o.Dir, "dir", "/run/ush/sessions", "")
	fs.StringVar(&o.Viewers, "viewers", "/run/ush/viewers", "")
	menu := fs.Bool("menu", false, "")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "ush: %v\r\n", err)
		return ExitNoJoin
	}
	if *menu {
		return Menu(o)
	}
	if strings.TrimSpace(o.Session) == "" {
		fmt.Fprint(os.Stderr, "ush: no session to join\r\n")
		return ExitNoJoin
	}
	return Join(o, os.Stdin, os.Stdout)
}
