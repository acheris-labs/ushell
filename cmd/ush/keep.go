package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"ush/internal/config"
	"ush/internal/keep"
)

// keepSubcommand is how ush runs its own keeper: itself, in the background,
// holding one command on a pty. Undocumented on purpose -- it is how -k works
// with --mux ush, not something to run by hand.
const keepSubcommand = "_keep"

// keepSharing is ush share -k. The command runs in a session named after the
// share, kept by ush itself or by tmux or herdr; what gets shared is only an
// attach to it.
// A rendezvous that goes away is the registration's problem: it reconnects on
// its own, and the attach never notices. So share returns only when the
// attach ends (the command exited, or the user detached), or with an error if
// sharing was impossible from the start, in which case it is retried here
// until Ctrl-C.
func keepSharing(cfg config.Config, o options, logPath string) error {
	tm, err := chooseMux(muxName(cfg, o), lookMux)
	if err != nil {
		return err
	}
	if o.name == "" {
		o.name = defaultName(cfg)
	}
	if !validName.MatchString(o.name) {
		return fmt.Errorf("invalid name %q: use 1-32 of A-Z a-z 0-9 . _ -, starting with a letter or digit", o.name)
	}
	if err := keepRefusal(tm, o); err != nil {
		return err
	}

	// Everything from here runs in ush's own terminal, not the multiplexer
	// ush was started from, which would otherwise refuse to nest.
	leaveEnclosingMultiplexer()
	if err := prepare(tm, o); err != nil {
		return err
	}

	// A backend that shares itself has already started doing so, in a process
	// that outlives this terminal. All that is left here is to look at it.
	if tm.sharesItself() {
		detached, err := keep.Attach(o.name, false, os.Stdin, os.Stdout)
		if err != nil {
			return err
		}
		if detached {
			fmt.Fprintf(os.Stderr, "\r\nush: detached; %q keeps running and stays shared (%s)\r\n",
				o.name, tm.reattachHint(o.name))
		}
		return nil
	}

	attach := o
	attach.command = tm.attachCommand(o.name)
	attach.guestCommand = tm.guestCommand(o.name, o.readOnly)

	var wait backoff
	for {
		started := time.Now()
		err := share(cfg, attach, logPath)
		if err == nil || !tm.exists(o.name) {
			return nil // the command exited or the user detached: done either way
		}
		delay := wait.next(time.Since(started))
		fmt.Fprintf(os.Stderr, "ush: %v\nush: %q is still running in %s; reconnecting in %s (ctrl-c stops; %s uses it here)\n",
			err, o.name, tm.name(), delay, tm.reattachHint(o.name))
		if !sleepUnlessInterrupted(delay) {
			fmt.Fprintf(os.Stderr, "ush: stopped; %q keeps running in %s (%s)\n", o.name, tm.name(), tm.reattachHint(o.name))
			return nil
		}
	}
}

// backoff paces reconnects: 2s, 4s, 8s, 16s, then every 30s, starting over
// after a connection that held for a minute.
type backoff struct{ last time.Duration }

func (b *backoff) next(connectedFor time.Duration) time.Duration {
	if b.last == 0 || connectedFor > time.Minute {
		b.last = 2 * time.Second
		return b.last
	}
	b.last = min(b.last*2, 30*time.Second)
	return b.last
}

func sleepUnlessInterrupted(d time.Duration) bool {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

func baseName(path string) string {
	return path[strings.LastIndex(path, "/")+1:]
}

// tmux runs the few tmux commands keep mode needs. Targets use "=name", an
// exact match; tmux otherwise matches prefixes ("api" would find "api-v2").
// prepare makes sure the session exists and is the one that was asked for,
// starting it if it is not there yet.
func prepare(tm keeper, o options) error {
	if !tm.exists(o.name) {
		if err := tm.create(o); err != nil {
			return fmt.Errorf("starting %s session %q: %w", tm.name(), o.name, err)
		}
		return nil
	}
	if !o.keepCommand {
		return nil // -k reattaches to whatever shell is there: that is the point
	}
	// The name is taken by something else. Sharing it anyway would hand out a
	// session nobody asked for, under a name the user thought was theirs.
	// The command runs as a job of $SHELL, which is what shows while it is
	// suspended.
	running := tm.currentCommand(o.name)
	if running == "" || running == baseName(o.command[0]) || (!o.noShell && running == baseName(os.Getenv("SHELL"))) {
		return nil
	}
	return fmt.Errorf("%q already exists in %s and is running %s, not %s\n"+
		"    attach to it with: %s\n"+
		"    or share under another name: -n SOMETHING-ELSE",
		o.name, tm.name(), running, baseName(o.command[0]), tm.reattachHint(o.name))
}

// keepRefusal is what a backend cannot do with these options.
func keepRefusal(tm keeper, o options) error {
	if o.keepCommand && !tm.canKeepCommand() {
		return fmt.Errorf("%s sessions don't end when their command does, so ush would never stop: keep a shell there, or use --mux ush or tmux", tm.name())
	}
	if tm.name() != "ush" && strings.ContainsAny(o.name, ".:") {
		return fmt.Errorf("%s session names can't contain . or :, so -k can't use the name %q there", tm.name(), o.name)
	}
	return nil
}

// keeper is a terminal multiplexer ush can hand the session to: it owns the
// command, so a rendezvous drop only ends the attach.
type keeper interface {
	name() string
	exists(session string) bool
	create(o options) error
	// sharesItself is true when the backend holds the share as well as the
	// session, so closing this terminal leaves both running.
	sharesItself() bool
	attachCommand(session string) []string
	// guestCommand is what each viewer runs on this machine, on a pty of its
	// own, instead of sharing the sharer's. It is nil when the backend has no
	// second client to give them. See TestGuestCommandGivesEachViewerItsOwnClient.
	guestCommand(session string, readOnly bool) []string
	canKeepCommand() bool // herdr sessions outlive their command, so it keeps a shell only
	currentCommand(session string) string
	reattachHint(session string) string // how to reach the session by hand
}

// muxName is what the user asked for: --mux, else the config's mux, else ""
// (which chooseMux takes to mean ush).
func muxName(cfg config.Config, o options) string {
	if o.mux != "" {
		return o.mux
	}
	return cfg.Mux
}

func lookMux(name string) (string, bool) {
	path, err := exec.LookPath(name)
	return path, err == nil
}

// chooseMux picks the backend: ush itself unless another was asked for, in
// which case it must be installed.
func chooseMux(asked string, look func(string) (string, bool)) (keeper, error) {
	make := map[string]func(string) keeper{
		"ush":   func(path string) keeper { return ushKeeper{path: path} },
		"tmux":  func(path string) keeper { return tmuxKeeper{path: path} },
		"herdr": func(path string) keeper { return herdrKeeper{path: path} },
	}
	if asked == "" {
		asked = "ush"
	}
	build, known := make[asked]
	if !known {
		return nil, fmt.Errorf("unknown multiplexer %q: ush knows ush, tmux, herdr", asked)
	}
	if asked == "ush" {
		// Not something to look up on PATH: it is this binary.
		self, err := os.Executable()
		if err != nil {
			return nil, err
		}
		return build(self), nil
	}
	path, ok := look(asked)
	if !ok {
		return nil, fmt.Errorf("--mux %s, but %s isn't installed", asked, asked)
	}
	return build(path), nil
}

// tmuxKeeper runs sessions in tmux.
type tmuxKeeper struct {
	path   string
	socket string // -L; only tests set it, to use a private server
}

func (t tmuxKeeper) name() string         { return "tmux" }
func (t tmuxKeeper) canKeepCommand() bool { return true }

func (t tmuxKeeper) reattachHint(session string) string { return "tmux attach -t " + session }

func (t tmuxKeeper) cmd(args ...string) *exec.Cmd {
	if t.socket != "" {
		args = append([]string{"-L", t.socket}, args...)
	}
	c := exec.Command(t.path, args...)
	c.Env = withoutTMUX(os.Environ())
	return c
}

func (t tmuxKeeper) exists(name string) bool {
	return t.cmd("has-session", "-t", "="+name).Run() == nil
}

// create starts the session detached, so it exists before anything is
// shared, with window-size "latest" so a small viewer can't shrink it.
func (tmuxKeeper) sharesItself() bool { return false }

func (t tmuxKeeper) create(o options) error {
	// A command runs as a job of $SHELL in the pane, as it would anywhere,
	// so ^Z drops to that shell rather than freezing the pane.
	l, err := commandLaunch(o)
	if err != nil {
		return err
	}
	args := []string{"new-session", "-d", "-s", o.name, "-e", "USH_SESSION=" + o.name}
	for _, kv := range l.env {
		args = append(args, "-e", kv)
	}
	args = append(args, l.argv...)
	name := o.name
	if out, err := t.cmd(args...).CombinedOutput(); err != nil {
		return errors.New(strings.TrimSpace(string(out)))
	}
	// window-size is a window option; "=name:" is that session's current window.
	return t.cmd("set-option", "-w", "-t", "="+name+":", "window-size", "latest").Run()
}

func (t tmuxKeeper) currentCommand(name string) string {
	out, err := t.cmd("display-message", "-p", "-t", "="+name+":", "#{pane_current_command}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (t tmuxKeeper) attachCommand(name string) []string {
	args := []string{t.path}
	if t.socket != "" {
		args = append(args, "-L", t.socket)
	}
	return append(args, "attach-session", "-t", "="+name)
}

// guestCommand attaches the viewer as a second tmux client. tmux then sizes
// the window to the latest client (window-size latest, set at create) rather
// than the plain shared pty being sized to the smallest terminal on it.
func (t tmuxKeeper) guestCommand(name string, readOnly bool) []string {
	args := []string{t.path}
	if t.socket != "" {
		args = append(args, "-L", t.socket)
	}
	args = append(args, "attach-session")
	if readOnly {
		args = append(args, "-r") // tmux's own read-only client
	}
	return append(args, "-t", "="+name)
}

// leaveEnclosingMultiplexer forgets the tmux or herdr this process was
// started from, for itself and everything it starts: the attach, and each
// viewer's own client. Both refuse to run inside themselves (herdr: "nested
// herdr is disabled by default"), and inherited, herdr's HERDR_SOCKET_PATH
// would point a new client at the enclosing server.
func leaveEnclosingMultiplexer() {
	os.Unsetenv("TMUX")
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "HERDR_") {
			os.Unsetenv(k)
		}
	}
}

func withoutTMUX(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, "TMUX=") {
			out = append(out, kv)
		}
	}
	return out
}

// herdrKeeper runs sessions in herdr. herdr creates a session when a client
// attaches, and its panes inherit that client's environment, so there is
// nothing to pre-create: the shared attach does it, with ush's USH_SESSION.
type herdrKeeper struct {
	path string
	run  func(args ...string) ([]byte, error) // replaced in tests
}

func (h herdrKeeper) name() string         { return "herdr" }
func (h herdrKeeper) canKeepCommand() bool { return false }

func (h herdrKeeper) output(args ...string) ([]byte, error) {
	if h.run != nil {
		return h.run(args...)
	}
	return exec.Command(h.path, args...).Output()
}

func (h herdrKeeper) exists(session string) bool {
	out, err := h.output("session", "list", "--json")
	if err != nil {
		return false
	}
	var listed struct {
		Sessions []struct {
			Name    string `json:"name"`
			Running bool   `json:"running"`
		} `json:"sessions"`
	}
	if json.Unmarshal(out, &listed) != nil {
		return false
	}
	for _, s := range listed.Sessions {
		if s.Name == session {
			return s.Running
		}
	}
	return false
}

func (herdrKeeper) sharesItself() bool { return false }

// create does nothing: herdr's attach creates the session if it is missing.
func (herdrKeeper) create(options) error { return nil }

func (h herdrKeeper) reattachHint(session string) string { return "herdr --session " + session }

func (h herdrKeeper) currentCommand(string) string { return "" }

// guestCommand attaches the viewer as a second herdr client. herdr's docs warn
// that only one writable client owns a terminal, but that is `herdr terminal
// attach`; `session attach` is a full client, and the sharer keeps working
// beside it. Without this a viewer received nothing at all: measured, a
// keystroke came back in 0.23s with it and never within 30s without.
//
// herdr has no read-only client to ask for, which is fine: a read-only
// session discards a guest's input whatever it is attached to.
func (h herdrKeeper) guestCommand(session string, _ bool) []string {
	return []string{h.path, "session", "attach", session}
}

func (h herdrKeeper) attachCommand(session string) []string {
	return []string{h.path, "--session", session}
}

// ushKeeper is ush keeping the session itself: a command on a pty held by a
// small background process of ours, with no tmux or herdr involved.
//
// It exists because the other two are terminal multiplexers first and
// session-keepers second: they bring windows, panes, status bars and their own
// key bindings to a job that needs none of it, and each has cost us a day this
// month for reasons that were entirely theirs. This keeps a screen, and that
// is all.
type ushKeeper struct{ path string }

func (ushKeeper) name() string { return "ush" }

func (ushKeeper) exists(session string) bool { return keep.Exists(session) }

// sharesItself is what makes ush different: the background process holds the
// share as well as the terminal, so closing the window that started it leaves
// the session listed and joinable.
func (ushKeeper) sharesItself() bool { return true }

func (u ushKeeper) create(o options) error {
	args := []string{keepSubcommand, "-n", o.name, "-profile", o.profile, "-share"}
	if o.noShell {
		args = append(args, "-no-shell")
	}
	if o.desc != "" {
		args = append(args, "-d", o.desc)
	}
	if o.readOnly {
		args = append(args, "-read-only")
	}
	cmd := exec.Command(u.path, append(append(args, "--"), o.command...)...)
	cmd.Env = append(os.Environ(), "USH_SESSION="+o.name)
	cmd.Dir, _ = os.Getwd()
	// Its own session, so closing this terminal does not hang up on it: that
	// is the whole point of keeping it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap it if we outlive it

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if keep.Exists(o.name) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("the kept session %q did not start", o.name)
}

func (u ushKeeper) attachCommand(session string) []string {
	return []string{u.path, "attach", session}
}

// guestCommand gives each viewer its own attachment, which is how a viewer's
// screen stays live and its size stays its own.
func (u ushKeeper) guestCommand(session string, readOnly bool) []string {
	// Flags before the name: Go's flag parsing stops at the first argument.
	if readOnly {
		return []string{u.path, "attach", "--read-only", session}
	}
	return []string{u.path, "attach", session}
}

// canKeepCommand is true: the session ends when its command does, so a kept
// command knows when to stop.
func (ushKeeper) canKeepCommand() bool { return true }

func (ushKeeper) currentCommand(session string) string {
	running := keep.Command(session)
	if running == "" {
		return ""
	}
	return baseName(strings.Fields(running)[0])
}

func (ushKeeper) reattachHint(session string) string { return "ush attach " + session }

// runKeeper is the background half of -k with ush keeping: this binary, holding one command on
// a pty until it exits, and -- when asked -- sharing it for that whole time.
//
// The share belongs here rather than in the terminal that started it. A
// terminal comes and goes; the point of keeping a session is that neither it
// nor its place in the menu goes with it.
func runKeeper(args []string) int {
	fs := flag.NewFlagSet("ush "+keepSubcommand, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("n", "", "")
	profile := fs.String("profile", "", "")
	desc := fs.String("d", "", "")
	readOnly := fs.Bool("read-only", false, "")
	shared := fs.Bool("share", false, "")
	noShell := fs.Bool("no-shell", false, "")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "ush: %v\n", err)
		return 2
	}

	logPath := defaultLogPath()
	if *shared {
		cfg, err := loadConfig(*profile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ush: %v\n", err)
			return 1
		}
		go shareForever(cfg, options{
			name:     *name,
			desc:     *desc,
			readOnly: *readOnly,
			// What is shared is an attachment to this session, and what the
			// menu shows is what is actually running in it.
			command:        []string{selfPath(), "attach", *name},
			guestCommand:   guestAttach(*name, *readOnly),
			displayCommand: fs.Args(),
		}, logPath)
	}

	w, h := 80, 24
	if cols, rows, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
		w, h = cols, rows
	}
	// The command runs as a job of $SHELL, so ^Z has somewhere to go; the
	// menu and ush attach --list still name the command itself.
	l, err := commandLaunch(options{command: fs.Args(), noShell: *noShell})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ush: %v\n", err)
		return 1
	}
	code, err := keep.Run(*name, l.argv, fs.Args(), append(os.Environ(), l.env...), keep.Size{Width: w, Height: h})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ush: %v\n", err)
		return 1
	}
	return code
}

// shareForever keeps the session in the menu for as long as it runs,
// reconnecting when the rendezvous goes away. It returns only if sharing is
// impossible, which leaves the session running and simply unlisted.
func shareForever(cfg config.Config, o options, logPath string) {
	for !keep.Exists(o.name) {
		time.Sleep(20 * time.Millisecond) // the pty comes up a moment later
	}
	logf := func(format string, args ...any) {
		f, err := openLog(logPath)
		if err != nil {
			return
		}
		defer f.Close()
		fmt.Fprintf(f, "ush keep %s: "+format+"\n", append([]any{o.name}, args...)...)
	}
	var wait backoff
	for {
		started := time.Now()
		err := share(cfg, o, logPath)
		if err == nil {
			logf("sharing ended")
			return
		}
		if !keep.Exists(o.name) {
			return // the session is gone; nothing left to share
		}
		delay := wait.next(time.Since(started))
		logf("%v; reconnecting in %s", err, delay)
		time.Sleep(delay)
	}
}

// guestAttach is what each viewer runs: its own attachment, so its screen is
// live and its size is its own.
func guestAttach(name string, readOnly bool) []string {
	return ushKeeper{path: selfPath()}.guestCommand(name, readOnly)
}

// selfPath is this binary, for the processes it starts on its own behalf.
func selfPath() string {
	if self, err := os.Executable(); err == nil {
		return self
	}
	return "ush"
}

// runAttach connects this terminal to a kept session. Ctrl-] leaves it running.
func runAttach(args []string) int {
	fs := flag.NewFlagSet("ush attach", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	readOnly := fs.Bool("read-only", false, "")
	list := fs.Bool("list", false, "")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "ush: %v\n", err)
		return 2
	}
	if *list {
		for _, name := range keep.List() {
			fmt.Printf("%-32s %s\n", name, keep.Command(name))
		}
		return 0
	}
	if fs.NArg() != 1 {
		names := keep.List()
		if len(names) == 0 {
			fmt.Fprintln(os.Stderr, "ush: no kept sessions (start one with: ush share -k -- COMMAND)")
		} else {
			fmt.Fprintf(os.Stderr, "ush: which session? %s\n", strings.Join(names, ", "))
		}
		return 2
	}
	detached, err := keep.Attach(fs.Arg(0), *readOnly, os.Stdin, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ush: %v\n", err)
		return 1
	}
	if detached {
		fmt.Fprintf(os.Stderr, "\r\nush: detached; %q keeps running (ush attach %s)\r\n", fs.Arg(0), fs.Arg(0))
	}
	return 0
}

// runStop ends a kept session and the command inside it.
func runStop(args []string) int {
	if len(args) != 1 {
		names := keep.List()
		if len(names) == 0 {
			fmt.Fprintln(os.Stderr, "ush: no kept sessions")
		} else {
			fmt.Fprintf(os.Stderr, "ush: which session? %s\n", strings.Join(names, ", "))
		}
		return 2
	}
	if err := keep.Stop(args[0]); errors.Is(err, keep.ErrNoSession) {
		fmt.Fprintf(os.Stderr, "ush: no session named %q\n", args[0])
		return 1
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "ush: %v\n", err)
		return 1
	}
	fmt.Printf("stopped %s\n", args[0])
	return 0
}
