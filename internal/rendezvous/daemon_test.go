package rendezvous

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"ush/internal/tunnel"
)

// These are the properties sshd_config used to hold. Configuration cannot
// regress quietly; code can, so each one is pinned here.

type daemonHarness struct {
	addr   string
	dir    string
	attach string
	signer ssh.Signer
	logs   *strings.Builder
}

func startDaemon(t *testing.T) *daemonHarness {
	t.Helper()
	return startDaemonRunning(t, "/nonexistent/ushd") // no test here starts a child
}

// startDaemonRunning starts a daemon whose session children are self.
func startDaemonRunning(t *testing.T, self string) *daemonHarness {
	t.Helper()

	_, loginKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(loginKey)
	if err != nil {
		t.Fatal(err)
	}
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	run := t.TempDir()
	// A unix socket path is capped at 104 bytes on macOS, and t.TempDir()
	// with a long test name is past that; so a short directory of our own.
	sock, err := os.MkdirTemp("", "ushd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sock) })

	logs := &strings.Builder{}
	d := &Daemon{
		Attach:  filepath.Join(sock, "attach.sock"),
		Dir:     filepath.Join(run, "sessions"),
		Viewers: filepath.Join(run, "viewers"),
		Key:     filepath.Join(run, "psk"),
		HostKey: hostKey,
		Login:   signer.PublicKey(),
		User:    "ush",
		Self:    self,
		Logger:  log.New(logs, "", 0),
		// Edits reach a sharer within a test's patience, not a person's.
		EditPoll: 20 * time.Millisecond,
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = d.Serve(ln) }()
	t.Cleanup(func() { _ = ln.Close() })

	return &daemonHarness{addr: ln.Addr().String(), dir: d.Dir, attach: d.Attach, signer: signer, logs: logs}
}

func (h *daemonHarness) dial(t *testing.T, user string, signer ssh.Signer) (*ssh.Client, error) {
	t.Helper()
	if signer == nil {
		signer = h.signer
	}
	return ssh.Dial("tcp", h.addr, &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	})
}

func (h *daemonHarness) must(t *testing.T) *ssh.Client {
	t.Helper()
	c, err := h.dial(t, "ush", nil)
	if err != nil {
		t.Fatalf("the one key should log in: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// run executes one command and returns stdout, stderr and the exit status.
func run(t *testing.T, c *ssh.Client, cmd string) (string, string, int) {
	t.Helper()
	sess, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	var out, errOut strings.Builder
	sess.Stdout = &out
	sess.Stderr = &errOut
	code := 0
	if err := sess.Run(cmd); err != nil {
		var exit *ssh.ExitError
		if !errorAs(err, &exit) {
			t.Fatalf("running %q: %v", cmd, err)
		}
		code = exit.ExitStatus()
	}
	return out.String(), errOut.String(), code
}

func errorAs(err error, target **ssh.ExitError) bool {
	e, ok := err.(*ssh.ExitError)
	if ok {
		*target = e
	}
	return ok
}

// startSharer registers a share whose viewers are handled by serve, and
// returns the session id the daemon assigned.
func startSharer(t *testing.T, h *daemonHarness, serve func(*tunnel.Guest)) string {
	t.Helper()
	c := h.must(t)
	viewers := c.HandleChannelOpen(tunnel.ChannelType)
	go func() {
		for nc := range viewers {
			g, err := tunnel.Accept(nc, func(tunnel.Size) {})
			if err == nil {
				go serve(g)
			}
		}
	}()
	sess, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	if err := sess.Start("share " + payload("name=api", "machine=umber")); err != nil {
		t.Fatal(err)
	}
	ack, _ := bufio.NewReader(stdout).ReadString('\n')
	m := regexp.MustCompile(`^registered api \(([A-Za-z0-9]{20})\)\n$`).FindStringSubmatch(ack)
	if m == nil {
		t.Fatalf("acknowledgement = %q", ack)
	}
	return m[1]
}

// echoTerm is a sharer that announces the viewer's TERM, then echoes.
func echoTerm(g *tunnel.Guest) {
	defer g.Close()
	_, _ = g.Write([]byte(g.Term + "\n"))
	_, _ = io.Copy(g, g)
}

func TestOnlyTheOneKeyLogsIn(t *testing.T) {
	h := startDaemon(t)

	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := ssh.NewSignerFromKey(other)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := h.dial(t, "ush", stranger); err == nil {
		c.Close()
		t.Fatal("a key that is not the seed's must be refused")
	}
	if c, err := h.dial(t, "root", nil); err == nil {
		c.Close()
		t.Fatal("the right key under another username must be refused")
	}
	h.must(t) // and the real one still works
}

func TestOnlyTheListedCommandsRun(t *testing.T) {
	c := startDaemon(t).must(t)
	for _, cmd := range []string{"shell", "sh -c 'id'", "bash", "cat /etc/passwd", "../../bin/sh"} {
		out, errOut, code := run(t, c, cmd)
		if code == 0 {
			t.Errorf("%q was accepted; output %q", cmd, out)
		}
		if !strings.Contains(errOut, "unsupported command") {
			t.Errorf("%q: stderr = %q, want an unsupported-command refusal", cmd, errOut)
		}
	}
}

// The client reads stderr to explain a refusal, so diagnostics must not be
// merged into stdout, which carries the acknowledgement or the JSON.
func TestRefusalsGoToStderrAndExitNonZero(t *testing.T) {
	c := startDaemon(t).must(t)
	out, errOut, code := run(t, c, "share not-base64")
	if code == 0 {
		t.Error("a bad registration must fail")
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if !strings.HasPrefix(errOut, "ush: ") {
		t.Errorf("stderr = %q, want a ush-prefixed message", errOut)
	}
}

func TestTheOldRegisterCommandSaysToUpgrade(t *testing.T) {
	c := startDaemon(t).must(t)
	_, errOut, code := run(t, c, "register "+payload("session=AbCdEfGhIjKlMnOpQrSt", "name=api"))
	if code == 0 || !strings.Contains(errOut, "upgrade ush") {
		t.Fatalf("exit %d, stderr %q; want a refusal that says to upgrade", code, errOut)
	}
}

func TestAViewerChannelReachesTheSharer(t *testing.T) {
	h := startDaemon(t)
	sid := startSharer(t, h, echoTerm)

	v, err := tunnel.Dial(h.must(t), sid, "vt220", tunnel.Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	r := bufio.NewReader(v)
	if line, _ := r.ReadString('\n'); line != "vt220\n" {
		t.Fatalf("the sharer saw TERM %q", line)
	}
	if _, err := v.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	if line, _ := r.ReadString('\n'); line != "ping\n" {
		t.Fatalf("echo = %q", line)
	}
}

func TestAViewerChannelOverTheAttachSocket(t *testing.T) {
	h := startDaemon(t)
	sid := startSharer(t, h, echoTerm)

	c, err := ssh.Dial("unix", h.attach, &ssh.ClientConfig{
		User:            "ush",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(h.signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	})
	if err != nil {
		t.Fatalf("the child's socket must take the same key: %v", err)
	}
	defer c.Close()
	v, err := tunnel.Dial(c, sid, "xterm", tunnel.Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if line, _ := bufio.NewReader(v).ReadString('\n'); line != "xterm\n" {
		t.Fatalf("the sharer saw TERM %q", line)
	}
}

func TestAViewerForAnUnknownSessionIsRefused(t *testing.T) {
	h := startDaemon(t)
	c := h.must(t)
	for _, sid := range []string{"AbCdEfGhIjKlMnOpQrSt", "../../etc/passwd", ""} {
		if _, err := tunnel.Dial(c, sid, "xterm", tunnel.Size{Cols: 80, Rows: 24}); err == nil {
			t.Errorf("session %q: a viewer was accepted", sid)
		}
	}
}

func TestAViewerEndsWhenTheShareEnds(t *testing.T) {
	h := startDaemon(t)
	c := h.must(t)
	viewers := c.HandleChannelOpen(tunnel.ChannelType)
	go func() {
		for nc := range viewers {
			if g, err := tunnel.Accept(nc, func(tunnel.Size) {}); err == nil {
				go func() { _, _ = io.Copy(g, g) }()
			}
		}
	}()
	sess, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	stdout, _ := sess.StdoutPipe()
	_, _ = sess.StdinPipe()
	if err := sess.Start("share " + payload("name=api")); err != nil {
		t.Fatal(err)
	}
	ack, _ := bufio.NewReader(stdout).ReadString('\n')
	sid := regexp.MustCompile(`\(([A-Za-z0-9]{20})\)`).FindStringSubmatch(ack)[1]

	v, err := tunnel.Dial(h.must(t), sid, "xterm", tunnel.Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	_ = c.Close() // the sharer is gone: SIGKILL, a dropped link
	if _, err := io.ReadAll(v); err != nil && err != io.EOF {
		t.Fatalf("viewer read ended with %v", err)
	}
	// ReadAll returned: the channel closed. That is the point.
}

// The menu needs a terminal. Without one it must say so and exit, not hang or
// run bubbletea against something that is not a terminal.
func TestTheMenuRefusesWithoutATerminal(t *testing.T) {
	c := startDaemon(t).must(t)
	done := make(chan struct{})
	var errOut string
	var code int
	go func() {
		defer close(done)
		_, errOut, code = run(t, c, "")
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the menu hung instead of refusing")
	}
	if code == 0 || !strings.Contains(errOut, "needs a terminal") {
		t.Fatalf("exit %d, stderr %q; want a refusal mentioning a terminal", code, errOut)
	}
}

func TestForwardingAndSubsystemsAreRefused(t *testing.T) {
	c := startDaemon(t).must(t)

	if conn, err := c.Dial("tcp", "127.0.0.1:22"); err == nil {
		conn.Close()
		t.Error("port forwarding must be refused")
	}
	if ln, err := c.Listen("tcp", "127.0.0.1:0"); err == nil {
		ln.Close()
		t.Error("remote forwarding must be refused")
	}

	sess, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.RequestSubsystem("sftp"); err == nil {
		t.Error("sftp must be refused")
	}
}

func TestRegistrationLivesAsLongAsItsConnection(t *testing.T) {
	h := startDaemon(t)
	c := h.must(t)

	sess, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	if err := sess.Start("share " + payload("name=api", "machine=umber")); err != nil {
		t.Fatal(err)
	}

	// The client waits for this line before handing over its terminal; the
	// id in it is the daemon's, not the sharer's.
	ack, _ := bufio.NewReader(stdout).ReadString('\n')
	m := regexp.MustCompile(`^registered api \(([A-Za-z0-9]{20})\)\n$`).FindStringSubmatch(ack)
	if m == nil {
		t.Fatalf("acknowledgement = %q", ack)
	}
	sid := m[1]
	if !strings.Contains(string(listJSON(h.dir, "")), `"session_id": "`+sid+`"`) {
		t.Fatal("the session should be listed while its connection is open")
	}

	_ = sess.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sessionGone(h.dir, sid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the entry outlived its connection")
}

func TestJoinChecksTheRegistry(t *testing.T) {
	c := startDaemon(t).must(t)
	for cmd, want := range map[string]string{
		"join":                      "bad session id",
		"join ../../etc/passwd":     "bad session id",
		"join AbCdEfGhIjKlMnOpQrSt": "no such session", // well-formed, never registered
	} {
		_, errOut, code := run(t, c, cmd)
		if code == 0 || !strings.Contains(errOut, want) {
			t.Errorf("%q: exit %d, stderr %q; want %q", cmd, code, errOut, want)
		}
	}
}

// The app joins every session over one connection, so closing one joined
// session's channel, not the connection, has to hang its child up; else the
// child, its pty and its viewer mark stay until the app goes away.
func TestClosingAJoinChannelHangsUpItsChild(t *testing.T) {
	dir := t.TempDir()
	pids := filepath.Join(dir, "pids")
	self := filepath.Join(dir, "child")
	script := "#!/bin/sh\necho $$ >> " + pids + "\nexec sleep 600\n"
	if err := os.WriteFile(self, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	h := startDaemonRunning(t, self)
	id := startSharer(t, h, echoTerm)
	c := h.must(t)

	joined := 0
	join := func() (*ssh.Session, int) {
		t.Helper()
		sess, err := c.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		if err := sess.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); err != nil {
			t.Fatal(err)
		}
		if err := sess.Start("join " + id); err != nil {
			t.Fatal(err)
		}
		var lines []string
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			b, _ := os.ReadFile(pids)
			if lines = strings.Fields(string(b)); len(lines) > joined {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if len(lines) <= joined {
			t.Fatal("the child never started")
		}
		joined++
		var pid int
		_, _ = fmt.Sscan(lines[joined-1], &pid)
		return sess, pid
	}
	alive := func(pid int) bool { return syscall.Kill(pid, 0) == nil }

	closing, gone := join()
	staying, kept := join()
	t.Cleanup(func() { _ = staying.Close() })
	_ = closing.Close()

	deadline := time.Now().Add(5 * time.Second)
	for alive(gone) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if alive(gone) {
		_ = syscall.Kill(gone, syscall.SIGKILL)
		t.Fatal("the child outlived its channel")
	}
	if !alive(kept) {
		t.Fatal("closing one channel hung up the other session too")
	}
}

func TestListIsTheRegistry(t *testing.T) {
	h := startDaemon(t)
	c := h.must(t)
	out, _, code := run(t, c, "list")
	if code != 0 {
		t.Fatalf("list exited %d", code)
	}
	if out != "[]\n" {
		t.Fatalf("an empty registry lists %q, want %q", out, "[]\n")
	}
}

func TestServeRefusesWithoutAuthentication(t *testing.T) {
	// Belt and braces around the SSH library's habit of treating "no auth
	// handler" as "no authentication required".
	if err := (&Daemon{}).Serve(nil); err == nil {
		t.Fatal("serving without a host key must fail")
	}
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Daemon{HostKey: hostKey, User: "ush"}).Serve(nil); err == nil {
		t.Fatal("serving without a login key must fail")
	}
}

func TestSessionPanicDoesNotEndOtherSessions(t *testing.T) {
	h := startDaemon(t)
	c := h.must(t)

	// A child that cannot be executed is the closest thing to a broken
	// session we can force from here; the daemon must survive it.
	_, _, _ = run(t, c, "join AbCdEfGhIjKlMnOpQrSt")

	out, _, code := run(t, c, "list")
	if code != 0 || out != "[]\n" {
		t.Fatalf("the daemon stopped serving after a failed session: %q (%d)", out, code)
	}
	if _, err := h.dial(t, "ush", nil); err != nil {
		t.Fatalf("new connections should still be accepted: %v", err)
	}
	_ = fmt.Sprint(h.logs.String()) // keep the logs reachable while debugging
}

func TestOneAddressCannotTakeEveryWaitingSlot(t *testing.T) {
	var w waiting
	for i := 0; i < maxPendingPerIP; i++ {
		if !w.take("192.0.2.1") {
			t.Fatalf("slot %d refused, want %d per address", i, maxPendingPerIP)
		}
	}
	if w.take("192.0.2.1") {
		t.Fatal("an address past its limit was let in")
	}
	if !w.take("192.0.2.2") {
		t.Fatal("another address was refused because of the first one")
	}
	w.done("192.0.2.1")
	if !w.take("192.0.2.1") {
		t.Fatal("a released slot was not reusable")
	}
}

func TestWaitingSlotsHaveAnOverallCap(t *testing.T) {
	var w waiting
	for i := 0; i < maxPending; i++ {
		if !w.take(fmt.Sprintf("192.0.2.%d", i)) {
			t.Fatalf("slot %d refused, want %d overall", i, maxPending)
		}
	}
	if w.take("198.51.100.1") {
		t.Fatal("a connection past the overall cap was let in")
	}
}

func TestSilentConnectionsFromOneAddressAreRefusedAndLogged(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	logs := make(chan string, 16)
	l := newLimitListener(ln, func(f string, a ...any) { logs <- fmt.Sprintf(f, a...) })
	defer l.Close()
	accepted := make(chan net.Conn, 16)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()

	var silent []net.Conn
	var held []net.Conn // the server's accepted side of each silent conn, holding a slot
	for i := 0; i < maxPendingPerIP; i++ {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		silent = append(silent, c)
		a := <-accepted
		_ = a.SetDeadline(time.Time{})
		held = append(held, a)
	}

	extra, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()
	extra.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := extra.Read(make([]byte, 1)); err == nil || isTimeout(err) {
		t.Fatalf("connection past the per-address limit: got %v, want it closed", err)
	}
	select {
	case line := <-logs:
		if !strings.Contains(line, "too many connections waiting") {
			t.Fatalf("log = %q", line)
		}
	case <-time.After(time.Second):
		t.Fatal("the refusal was not logged")
	}

	// Closing an accepted connection must give its slot back: a scanner that
	// fills every slot and then drops must not lock its address out forever.
	_ = held[0].Close()
	deadline := time.Now().Add(time.Second)
	for {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		select {
		case a := <-accepted:
			_ = a.Close()
			return
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("the freed slot was never reused")
		}
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func TestSharersFreeTheirSlotOnceRegistered(t *testing.T) {
	// A share lasts hours; it must not hold a waiting slot for that long.
	h := startDaemon(t)
	for i := 0; i < maxPendingPerIP+1; i++ {
		startSharer(t, h, func(g *tunnel.Guest) { _ = g.Close() })
	}
	c := h.must(t) // would be locked out if the shares still held slots
	if _, _, code := run(t, c, "list"); code != 0 {
		t.Fatalf("list exited %d", code)
	}
}

func TestUnauthenticatedConnectionsKeepTheirSlot(t *testing.T) {
	// MaxStartups: a client that sent its version line and then stalls in the
	// handshake still counts, so one address cannot pile up handshakes.
	h := startDaemon(t)
	for i := 0; i < maxPendingPerIP; i++ {
		c, err := net.Dial("tcp", h.addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.Write([]byte("SSH-2.0-OpenSSH_9.8\r\n"))
	}
	time.Sleep(100 * time.Millisecond) // let them reach the SSH server
	if c, err := h.dial(t, "ush", nil); err == nil {
		c.Close()
		t.Fatal("stalled handshakes did not count against the limit")
	}
}

func TestAuthenticatedConnectionsFreeTheirSlot(t *testing.T) {
	h := startDaemon(t)
	for i := 0; i < maxPendingPerIP+1; i++ {
		c := h.must(t) // fails the test if this address has been locked out
		if _, _, code := run(t, c, "list"); code != 0 {
			t.Fatalf("list exited %d", code)
		}
	}
}

// An authenticated connection that never opens a channel (ssh -N) is past
// the handshake timeout's reach, so the slot must come back on its own.
func TestSlotsComeBackAfterTheHandshakeTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := newLimitListener(ln, func(string, ...any) {})
	l.release = 100 * time.Millisecond
	defer l.Close()
	accepted := make(chan net.Conn, maxPendingPerIP+1)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	for i := 0; i < maxPendingPerIP; i++ {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		<-accepted // held open, never freed by a handler
	}
	time.Sleep(200 * time.Millisecond)
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	select {
	case a := <-accepted:
		_ = a.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("slots held past the handshake timeout were never released")
	}
}

func TestEveryRateLimitsAndCounts(t *testing.T) {
	var got []string
	logf := func(f string, a ...any) { got = append(got, fmt.Sprintf(f, a...)) }
	e := &every{period: 50 * time.Millisecond}
	e.logf(logf, "x %d", 1)
	e.logf(logf, "x %d", 2)
	e.logf(logf, "x %d", 3)
	time.Sleep(60 * time.Millisecond)
	e.logf(logf, "x %d", 4)
	want := []string{"x 1", "x 4 (and 2 more since the last report)"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("logged %q, want %q", got, want)
	}
}

func TestDescribeSetsAndClearsADescription(t *testing.T) {
	h := startDaemon(t)
	c := h.must(t)
	if err := writeEntry(h.dir, api); err != nil {
		t.Fatal(err)
	}
	text := base64.StdEncoding.EncodeToString([]byte("pr monitor \x1b[31m"))
	if _, errOut, code := run(t, c, "describe "+api.ID+" "+text); code != 0 {
		t.Fatalf("describe exited %d: %s", code, errOut)
	}
	if got, _ := readSession(h.dir, api.ID); got.Description != "pr monitor [31m" {
		t.Fatalf("description = %q, want it set with the escape stripped", got.Description)
	}
	if _, errOut, code := run(t, c, "describe "+api.ID); code != 0 {
		t.Fatalf("describe with no text exited %d: %s", code, errOut)
	}
	if got, _ := readSession(h.dir, api.ID); got.Description != "" || got.Name != "api" {
		t.Fatalf("after clearing: %+v", got)
	}
}

func TestDescribeChecksItsArguments(t *testing.T) {
	h := startDaemon(t)
	c := h.must(t)
	if err := writeEntry(h.dir, api); err != nil {
		t.Fatal(err)
	}
	for cmd, want := range map[string]string{
		"describe":                              "expected a session id",
		"describe ../../etc/passwd eA==":        "expected a session id",
		"describe AbCdEfGhIjKlMnOpQrSt eA==":    "no such session",
		"describe " + api.ID + " not-base64!":   "not base64",
		"describe " + api.ID + " eA== too many": "expected a session id",
	} {
		_, errOut, code := run(t, c, cmd)
		if code == 0 || !strings.Contains(errOut, want) {
			t.Errorf("%q: exit %d, stderr %q; want %q", cmd, code, errOut, want)
		}
	}
	if !sessionGone(h.dir, "AbCdEfGhIjKlMnOpQrSt") {
		t.Error("describing an unknown session must not create it")
	}
}

func TestEditsGoBackToTheSharer(t *testing.T) {
	h := startDaemon(t)
	c := h.must(t)
	sess, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	if err := sess.Start("share " + payload("name=api", "description=old", "machine=umber")); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(stdout)
	ack, _ := r.ReadString('\n')
	m := regexp.MustCompile(`^registered api \(([A-Za-z0-9]{20})\)\n$`).FindStringSubmatch(ack)
	if m == nil {
		t.Fatalf("acknowledgement = %q", ack)
	}

	next := func() string {
		t.Helper()
		lines := make(chan string, 1)
		go func() { l, _ := r.ReadString('\n'); lines <- l }()
		select {
		case l := <-lines:
			blob, ok := strings.CutPrefix(strings.TrimSuffix(l, "\n"), "update ")
			raw, err := base64.StdEncoding.DecodeString(blob)
			if !ok || err != nil {
				t.Fatalf("not an update line: %q", l)
			}
			return string(raw)
		case <-time.After(5 * time.Second):
			t.Fatal("no update reached the sharer")
			return ""
		}
	}
	if err := describeSession(h.dir, m[1], "pr monitor"); err != nil {
		t.Fatal(err)
	}
	if got := next(); got != "name=api\ndescription=pr monitor\n" {
		t.Fatalf("after describe: %q", got)
	}
	if err := renameSession(h.dir, m[1], "api-v2"); err != nil {
		t.Fatal(err)
	}
	if got := next(); got != "name=api-v2\ndescription=pr monitor\n" {
		t.Fatalf("after rename: %q", got)
	}
}

func TestTheSharerCanRelabelItsShare(t *testing.T) {
	h := startDaemon(t)
	c := h.must(t)
	sess, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Start("share " + payload("name=api", "machine=umber")); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(stdout)
	ack, _ := r.ReadString('\n')
	m := regexp.MustCompile(`^registered api \(([A-Za-z0-9]{20})\)\n$`).FindStringSubmatch(ack)
	if m == nil {
		t.Fatalf("acknowledgement = %q", ack)
	}
	update := func(raw string) {
		t.Helper()
		if _, err := fmt.Fprintf(stdin, "update %s\n", base64.StdEncoding.EncodeToString([]byte(raw))); err != nil {
			t.Fatal(err)
		}
	}
	waitFor := func(name, desc string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if s, _ := readSession(h.dir, m[1]); s.Name == name && s.Description == desc {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		s, _ := readSession(h.dir, m[1])
		t.Fatalf("entry is %q, %q; want %q, %q", s.Name, s.Description, name, desc)
	}

	update("name=api-v2\ndescription=pr monitor\x1b[31m\n")
	waitFor("api-v2", "pr monitor[31m")
	update("name=../../etc\ndescription=still here\n") // a bad name is ignored, not the line
	waitFor("api-v2", "still here")

	// The sharer hears its entry as saved, its own edits included, and then
	// the menu's: the last line it gets is always the entry as it is.
	if err := describeSession(h.dir, m[1], "from the menu"); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 64) // never blocks the reader once the test is done
	go func() {
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				return
			}
			select {
			case lines <- l:
			default:
			}
		}
	}()
	var heard []string
	for {
		select {
		case l := <-lines:
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(strings.TrimSpace(l), "update "))
			heard = append(heard, string(raw))
			if string(raw) == "name=api-v2\ndescription=from the menu\n" {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("the menu's edit never reached the sharer; it heard %q", heard)
		}
	}
}
