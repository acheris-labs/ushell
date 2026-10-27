// ush shares a terminal through a rendezvous under a human name, so it shows
// up in the rendezvous menu instead of as a random session ID.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"

	"ush/internal/config"
	"ush/internal/detach"
	"ush/internal/hostkey"
	"ush/internal/psk"
	"ush/internal/register"
	"ush/internal/sharer"
)

// validName starts with a letter or digit, so a name is never read as a flag
// by ush attach, herdr or tmux.
var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

// nameMisfits are the runs defaultName turns into a dash; dots too, which
// tmux session names can't hold.
var nameMisfits = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

const usage = `usage: ush [--profile NAME]
       ush share [-k [--mux ush|tmux|herdr]] [-n NAME] [-d DESCRIPTION] [--read-only]
                 [--no-shell] [--profile NAME] [-- COMMAND [ARGS...]]
       ush connect [NAME|ID [-m MACHINE]] [--profile NAME]
       ush connect --list [-m MACHINE] [--profile NAME]
       ush adopt PID [-n NAME] [-d DESCRIPTION] [--read-only] [--profile NAME]
       ush attach [NAME] [--read-only] [--list]
       ush stop NAME
       ush init [--help]
       ush seed
       ush [--profile NAME] ping
       ush [--profile NAME] pair

ush on its own opens the rendezvous menu of live sessions, and picking one
joins it. ush connect NAME joins that session without the menu; when two
share a name, -m MACHINE picks the one shared from there (or connect by ID).
ush connect alone is the menu; ush connect --list prints what is live. ush
share shares COMMAND (default: $SHELL) and lists it in the menu under NAME;
ush serve and ush -s are the same thing.

Sharing:
  -n, --name NAME          session name (default: the machine name)
  -d, --description TEXT   shown in the menu's detail pane
      --read-only          viewers may watch but not type
      --no-shell           run COMMAND on its own. By default it runs as the
                           first job of your $SHELL (zsh or bash), so ^Z drops
                           to that shell and fg brings it back
  -k, --keep               keep COMMAND (else your shell) running when this
                           window closes or the rendezvous restarts
      --mux NAME           what keeps it:
                             ush    the default. Keeps the share too, so it
                                    stays listed with the window closed;
                                    ush attach NAME gets back to it
                             tmux   keeps the command; the share lasts while a
                             herdr  window runs ush share -k for it, and running
                                    that again reattaches. herdr: a shell only

Everywhere:
      --profile NAME       profile from the config (default: $USH_PROFILE,
                           then the config's default = "...")

ush adopt moves a process that is already running into a share, in this
window, for when it was started outside ush. macOS only, and only for
processes a debugger may attach to: not Apple's own binaries (/bin/bash,
/usr/bin/*) or hardened apps (claude). ^C, ^Z and hangup still come from its
old window, and its children stay there.

ush attach reconnects to a kept session (--list shows them); ush stop ends one.
Ctrl-] leaves a session running, whether you are attached or joined.
ush init adds a profile to the config, and generates the key if needed.
ush seed prints a new random seed for USH_SEED / ush init --seed.
ush ping checks that the rendezvous can be reached and logged in to.
ush pair shows a profile as a QR code for the iOS app. It holds the key.

Config: ~/.ushrc, or the file named by $USH_RC.
Log:    %s
`

type options struct {
	share       bool
	profile     string
	name        string
	desc        string
	readOnly    bool
	mux         string // --mux: what keeps a -k session: ush, tmux or herdr
	keep        bool   // -k: the session outlives this window and rendezvous restarts
	keepCommand bool   // -k with a command: the session ends when the command does
	noShell     bool   // --no-shell: run the command bare, not as a job of $SHELL
	adopt       int    // ush adopt: move this running process onto the share instead
	command     []string
	target      string // ush connect: the session name or id to join directly
	machine     string // ush connect -m: only sessions shared from this machine
	list        bool   // ush connect --list: print the live sessions instead
	// guestCommand runs per viewer, on its own pty, instead of handing them
	// the sharer's. Keep mode fills it in when the multiplexer allows a
	// second client; empty means every viewer shares the one pty.
	guestCommand []string
	// displayCommand is what the menu shows. In keep mode the shared command
	// is an attach, which says nothing about what is actually running.
	displayCommand []string
	// env is added to command's environment: how $SHELL is told what to run.
	env []string
}

func main() {
	os.Exit(run())
}

// nestedIn names the shared session this terminal is already part of, if any.
// Every shared command runs with USH_SESSION set, so a -s or -c from inside
// one would share a session with itself: the sharer would relay its own
// viewer, and a viewer would join the terminal it is already looking at.
// Subcommands that do not take over the terminal (ping, init, seed, help) are
// unaffected, and run before this.
func nestedIn(getenv func(string) string) string {
	return getenv("USH_SESSION")
}

func run() int {
	logPath := defaultLogPath()
	if len(os.Args) > 1 && os.Args[1] == "init" {
		return runInit(os.Args[2:])
	}
	if i := subcommand(os.Args[1:], "ping"); i >= 0 {
		return runPing(append(os.Args[1:i+1:i+1], os.Args[i+2:]...), os.Stdout)
	}
	if i := subcommand(os.Args[1:], "pair"); i >= 0 {
		return runPair(append(os.Args[1:i+1:i+1], os.Args[i+2:]...))
	}
	if len(os.Args) > 1 && os.Args[1] == keepSubcommand {
		return runKeeper(os.Args[2:])
	}
	if len(os.Args) > 1 && os.Args[1] == "attach" {
		return runAttach(os.Args[2:])
	}
	if len(os.Args) > 1 && os.Args[1] == "stop" {
		return runStop(os.Args[2:])
	}
	if len(os.Args) == 2 && os.Args[1] == "seed" {
		s, err := psk.NewSeed()
		if err != nil {
			fmt.Fprintf(os.Stderr, "ush: %v\n", err)
			return 1
		}
		fmt.Println(s)
		return 0
	}
	o, err := parseArgs(os.Args[1:], os.Getenv)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(os.Stderr, usage, logPath)
		return 0
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ush: %v\n", err)
		return 2
	}

	// Listing takes nothing over, so it is fine from inside a session.
	if name := nestedIn(os.Getenv); name != "" && !o.list {
		fmt.Fprintf(os.Stderr, "ush: this terminal is already inside ush session %q\n"+
			"    sharing or connecting from here nests a session inside itself\n"+
			"    (unset USH_SESSION if you really mean to)\n", name)
		return 1
	}

	cfg, err := loadConfig(o.profile)
	if err == nil {
		switch {
		case o.share && o.keep:
			err = keepSharing(cfg, o, logPath)
		case o.share && o.adopt != 0:
			o.displayCommand = commandOf(o.adopt)
			err = share(cfg, o, logPath)
		case o.share:
			if o, err = inOwnShell(o); err == nil {
				err = share(cfg, o, logPath)
			}
		case o.list:
			err = listLive(cfg, o.machine, os.Stdout)
		default:
			var code int
			if code, err = connect(cfg, o.target, o.machine); err == nil && code != 0 {
				return code
			}
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ush: %v\n", err)
		return 1
	}
	return 0
}

// parseArgs reads the arguments for one of the two modes. Sharing is its own
// subcommand: `ush share`. Anything else is connecting, which takes only the
// options that make sense for looking at someone else's terminal.
//
// The two used to be -s and -c on one flag set, which meant every sharing
// option had to be policed on the connect path ("sharing is explicit: add
// -s"). A subcommand says it once, in the spelling. -s is back only as one
// of those spellings, and only in first place, like tar -c.
func parseArgs(args []string, getenv func(string) string) (options, error) {
	if len(args) > 0 && slices.Contains(shareSpellings, args[0]) {
		return parseShare(args[1:], getenv)
	}
	if len(args) > 0 && args[0] == "adopt" {
		return parseAdopt(args[1:], getenv)
	}
	if i := subcommand(args, "connect"); i >= 0 {
		return parseConnect(append(args[:i:i], args[i+1:]...), getenv, true)
	}
	o, err := parseConnect(args, getenv, false)
	if err != nil {
		if i := slices.IndexFunc(args, func(a string) bool { return slices.Contains(shareSpellings, a) }); i > 0 {
			return options{}, fmt.Errorf("%w: to share, %s goes first (ush %s ...)", err, args[i], args[i])
		}
	}
	return o, err
}

// shareSpellings all mean `ush share`, so there is no wrong one to remember.
var shareSpellings = []string{"share", "serve", "-s"}

// parseConnect reads the connect path. Only `ush connect` takes a target, a
// session name or id; flags may come before or after it.
func parseConnect(args []string, getenv func(string) string, takesTarget bool) (options, error) {
	var o options
	fs := flag.NewFlagSet("ush", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.profile, "profile", "", "")
	if takesTarget {
		fs.StringVar(&o.machine, "m", "", "")
		fs.StringVar(&o.machine, "machine", "", "")
		fs.BoolVar(&o.list, "list", false, "")
	}
	for {
		if err := fs.Parse(args); err != nil {
			return options{}, err
		}
		if fs.NArg() == 0 {
			break
		}
		switch {
		case !takesTarget:
			return options{}, fmt.Errorf("unexpected argument %q: to share a terminal, use ush share", fs.Arg(0))
		case o.target != "":
			return options{}, fmt.Errorf("unexpected argument %q: ush connect takes one session", fs.Arg(0))
		}
		o.target = fs.Arg(0)
		args = fs.Args()[1:]
	}
	if o.list && o.target != "" {
		return options{}, fmt.Errorf("--list lists every session: drop %q", o.target)
	}
	if o.machine != "" && o.target == "" && !o.list {
		return options{}, errors.New("-m picks between sessions of one name: ush connect NAME -m MACHINE")
	}
	if o.profile == "" {
		o.profile = getenv("USH_PROFILE")
	}
	return o, nil
}

func parseShare(args []string, getenv func(string) string) (options, error) {
	o := options{share: true}
	fs := flag.NewFlagSet("ush share", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.name, "n", "", "")
	fs.StringVar(&o.name, "name", "", "")
	fs.StringVar(&o.desc, "d", "", "")
	fs.StringVar(&o.desc, "description", "", "")
	fs.BoolVar(&o.readOnly, "read-only", false, "")
	fs.StringVar(&o.profile, "profile", "", "")
	fs.BoolVar(&o.keep, "k", false, "")
	fs.BoolVar(&o.keep, "keep", false, "")
	fs.BoolVar(&o.noShell, "no-shell", false, "")
	fs.StringVar(&o.mux, "mux", "", "")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if o.profile == "" {
		o.profile = getenv("USH_PROFILE")
	}

	o.command = fs.Args()
	// -k keeps whatever it is given: the command, or else the shell.
	o.keepCommand = o.keep && len(o.command) > 0
	if o.mux != "" && !o.keep {
		return options{}, errors.New("--mux only applies with -k")
	}
	if len(o.command) == 0 {
		shell := getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		o.command = []string{shell}
	}
	return o, nil
}

// parseAdopt reads ush adopt: the sharing options that still make sense for
// a process that is already running, and its pid.
func parseAdopt(args []string, getenv func(string) string) (options, error) {
	o := options{share: true}
	fs := flag.NewFlagSet("ush adopt", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.name, "n", "", "")
	fs.StringVar(&o.name, "name", "", "")
	fs.StringVar(&o.desc, "d", "", "")
	fs.StringVar(&o.desc, "description", "", "")
	fs.BoolVar(&o.readOnly, "read-only", false, "")
	fs.StringVar(&o.profile, "profile", "", "")
	var pids []string
	for {
		if err := fs.Parse(args); err != nil {
			return options{}, err
		}
		if fs.NArg() == 0 {
			break
		}
		pids = append(pids, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(pids) != 1 {
		return options{}, errors.New("ush adopt takes one PID")
	}
	pid, err := strconv.Atoi(pids[0])
	if err != nil || pid <= 0 {
		return options{}, fmt.Errorf("invalid PID %q", pids[0])
	}
	o.adopt = pid
	if o.profile == "" {
		o.profile = getenv("USH_PROFILE")
	}
	return o, nil
}

// subcommand finds name among args, allowing it after --profile NAME, so both
// `ush ping --profile x` and `ush --profile x ping` work. Returns its index,
// or -1.
func subcommand(args []string, name string) int {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == name:
			return i
		case args[i] == "--profile" || args[i] == "-profile":
			i++ // skip its value
		case strings.HasPrefix(args[i], "--profile=") || strings.HasPrefix(args[i], "-profile="):
		default:
			return -1
		}
	}
	return -1
}

// loadSigner reads the profile's key and nothing else: never the agent's
// keys, which on a machine with an agent would be offered first and
// rejected.
func loadSigner(path string) (ssh.Signer, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(pem)
	var locked *ssh.PassphraseMissingError
	if errors.As(err, &locked) {
		return nil, fmt.Errorf("%s is protected by a passphrase; ush needs an unencrypted key (ush init --seed writes one)", path)
	}
	if err != nil {
		return nil, fmt.Errorf("reading key %s: %w", path, err)
	}
	return signer, nil
}

func loadConfig(profile string) (config.Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return config.Config{}, err
	}
	rc := os.Getenv("USH_RC")
	if rc == "" {
		rc = filepath.Join(home, ".ushrc")
	}
	return config.Load(rc, home, profile)
}

// commandOf is pid's command line, as ps shows it.
func commandOf(pid int) []string {
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return []string{"pid " + strconv.Itoa(pid)}
	}
	return []string{strings.TrimSpace(string(out))}
}

// shown is what the menu lists: the real command when keep mode is relaying
// an attach, and otherwise what was asked for.
func shown(o options) []string {
	if len(o.displayCommand) > 0 {
		return o.displayCommand
	}
	return o.command
}

func share(cfg config.Config, o options, logPath string) error {
	name, desc, readOnly, command := o.name, o.desc, o.readOnly, o.command
	if name == "" {
		name = defaultName(cfg)
	}
	if !validName.MatchString(name) {
		return fmt.Errorf("invalid name %q: use 1-32 of A-Z a-z 0-9 . _ -, starting with a letter or digit", name)
	}

	// A log that cannot be opened costs the log, not the share.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if logFile, err := openLog(logPath); err == nil {
		defer logFile.Close()
		logger = slog.New(slog.NewTextHandler(logFile, nil))
	}
	// Every share writes to the one log; this says which share each line is.
	logger = logger.With("session", name)

	signer, err := loadSigner(cfg.Key)
	if err != nil {
		return err
	}
	hostKeys, err := hostKeyCallback(cfg)
	if err != nil {
		return err
	}

	cwd, _ := os.Getwd()
	info := register.Info{
		Name:        name,
		Description: desc,
		Machine:     cfg.Machine,
		Command:     strings.Join(shown(o), " "),
		Cwd:         cwd,
		User:        currentUser(),
		ReadOnly:    readOnly,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	regCtx, endRegistration := context.WithCancel(ctx)
	defer endRegistration()

	sess := sharer.New(sharer.Options{
		Command:      command,
		Env:          o.env,
		GuestCommand: o.guestCommand,
		ReadOnly:     readOnly,
		Adopt:        o.adopt,
		Stdin:        os.Stdin,
		Stdout:       os.Stdout,
		Logger:       logger,
	})
	addr := net.JoinHostPort(cfg.Rendezvous, cfg.Port)
	// Register first: a rendezvous that refuses or cannot be reached is
	// reported before the terminal is taken over. The registration is also
	// the tunnel; viewers arrive on it from here on.
	err = register.Register(regCtx, register.Target{
		Addr: addr,
		Config: &ssh.ClientConfig{
			User:            cfg.User,
			Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
			HostKeyCallback: hostKeys,
		},
		Command:  register.Command(info),
		OnViewer: sess.Accept,
	}, func(format string, args ...any) {
		logger.Info(fmt.Sprintf(format, args...))
	})
	if err != nil {
		if ctx.Err() != nil {
			// SIGTERM/SIGHUP landed while Register was still connecting
			// (against a blackholed host, up to handshakeTimeout): a clean
			// exit, not a failure to retry.
			return nil
		}
		if !errors.Is(err, register.ErrRetrying) {
			return shareError(addr, ssh.FingerprintSHA256(signer.PublicKey()), err)
		}
		fmt.Fprintf(os.Stderr, "ush: rendezvous %s: %s\n"+
			"ush: %q runs anyway, and is listed once the rendezvous can be reached\n",
			addr, explain(err), name)
	}

	// The command runs with this process's environment, so this tells shells
	// and prompts they are being shared, and under what name.
	if err := os.Setenv("USH_SESSION", name); err != nil {
		return err
	}
	err = sess.Run(ctx)
	// The command may have died, or been left running by a SIGTERM, with the
	// mouse on in this terminal.
	if term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprint(os.Stdout, detach.ResetModes)
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// shareError explains why registration failed and names addr, so a wrong
// host/port or a mistaken profile is never a mystery. Two shapes already
// name themselves and are left untouched: register.rejection's own
// "rendezvous rejected registration: ..." and "the rendezvous is older than
// this ush: redeploy it" (internal/register/register.go); prefixing addr in
// front of either would repeat "rendezvous" as the very next word. A
// rejected key gets fingerprint spelled out here rather than routed through
// explain's "ush ping" wording, which says to compare it with the
// fingerprint printed "above" -- true on the ping path, meaningless here,
// since share never prints one.
func shareError(addr, fingerprint string, err error) error {
	if err == nil {
		return err
	}
	msg := err.Error()
	if msg == "the rendezvous is older than this ush: redeploy it" || strings.HasPrefix(msg, "rendezvous rejected registration:") {
		return err
	}
	if strings.Contains(msg, "unable to authenticate") {
		return fmt.Errorf("rendezvous %s: your key (%s) was rejected: it is not the rendezvous's key; check that fingerprint against the rendezvous's log", addr, fingerprint)
	}
	return fmt.Errorf("rendezvous %s: %s", addr, explain(err))
}

// hostKeyCallback verifies the rendezvous's host key: trust on first use,
// rejecting a changed key. USH_SKIP_HOST_KEY_CHECK=1 / skip_host_key_check
// disables checking outright, for a local test rendezvous whose keys are
// regenerated on every rebuild.
func hostKeyCallback(cfg config.Config) (ssh.HostKeyCallback, error) {
	if cfg.SkipHostKeyCheck {
		return ssh.InsecureIgnoreHostKey(), nil
	}
	return hostkey.TOFU(cfg.KnownHosts)
}

// defaultName names a session shared without -n: the machine name, fitted
// to a session name ("Alex's MacBook" becomes Alex-s-MacBook).
func defaultName(cfg config.Config) string {
	n := strings.Trim(nameMisfits.ReplaceAllString(cfg.Machine, "-"), "-_")
	if len(n) > 32 {
		n = strings.TrimRight(n[:32], "-")
	}
	if n == "" {
		return "unknown"
	}
	return n
}

// defaultLogPath is ~/.ush/ush.log, beside the kept sessions. The system temp
// directory is shared on Linux, where another user could create the log first
// and so stop this one's shares, or point it elsewhere with a symlink.
func defaultLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ush", "ush.log")
}

// openLog opens the log for appending, in a directory only this user can
// reach, and never through a symlink.
func openLog(path string) (*os.File, error) {
	if path == "" {
		return nil, errors.New("no home directory for the log")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
}

func currentUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return os.Getenv("USER")
}
