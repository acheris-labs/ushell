package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"ush/internal/config"
	"ush/internal/detach"
)

// browse runs ssh to the rendezvous and returns its exit code. The forced
// command there shows the menu and, on selection, joins the session within
// the same connection. System ssh already does terminal handling, resize, and
// known_hosts properly, so there is nothing to gain by reimplementing it.
//
// It runs as a child rather than in this process's place so that its end can
// be cleaned up after: a connection that drops never delivers the menu's own
// cleanup, and would leave the terminal reporting the mouse.
//
// remote, if given, is run there instead of the menu: `join ID` for ush
// connect NAME.
func browse(cfg config.Config, remote ...string) (int, error) {
	path, err := exec.LookPath("ssh")
	if err != nil {
		return 0, fmt.Errorf("browsing needs ssh on PATH: %w", err)
	}
	cmd := exec.Command(path)
	cmd.Args = browseArgs(cfg, remote...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	// The terminal sends Ctrl-C and friends to ssh as well, and ssh decides
	// what they mean; this process only stays alive to clean up. A signal
	// meant for this process alone is passed on.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	go func() {
		for sig := range sigs {
			if sig == syscall.SIGTERM || sig == syscall.SIGHUP {
				_ = cmd.Process.Signal(sig)
			}
		}
	}()
	err = cmd.Wait()

	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		return 0, err
	}
	resetAfterBrowse(os.Stdout, term.IsTerminal(int(os.Stdout.Fd())), code)
	return code, nil
}

// resetAfterBrowse turns off whatever the session left on. ssh exits 255 when
// the connection fails, and then the menu's own cleanup never arrived, so the
// terminal is still on the alternate screen too; after any other exit it has
// already left, and leaving again would restore a stale cursor position.
func resetAfterBrowse(out io.Writer, isTerminal bool, code int) {
	if !isTerminal {
		return
	}
	reset := detach.ResetModes
	if code == 255 {
		reset += "\x1b[?1049l"
	}
	_, _ = io.WriteString(out, reset)
}

func browseArgs(cfg config.Config, remote ...string) []string {
	args := []string{
		"ssh", "-t",
		"-i", cfg.Key,
		"-p", cfg.Port,
		"-o", "IdentitiesOnly=yes",
		"-o", "LogLevel=ERROR",
	}
	if cfg.SkipHostKeyCheck {
		args = append(args,
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null")
	} else {
		args = append(args,
			"-o", "StrictHostKeyChecking=accept-new",
			"-o", "UserKnownHostsFile="+cfg.KnownHosts)
	}
	// JoinHostPort would bracket an IPv6 literal, which ssh does not want in
	// user@host form; strip the port it adds.
	host, _, _ := net.SplitHostPort(net.JoinHostPort(cfg.Rendezvous, "0"))
	return append(append(args, cfg.User+"@"+host), remote...)
}
