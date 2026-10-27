package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestInShellLeavesAloneWhatItShould(t *testing.T) {
	home := t.TempDir()
	for _, c := range []struct {
		why     string
		shell   string
		command []string
	}{
		{"the command is a shell already", "/bin/zsh", []string{"/bin/bash"}},
		{"the command is $SHELL itself", "/bin/zsh", []string{"/bin/zsh"}},
		{"a shell ush does not know", "/usr/local/bin/nu", []string{"claude"}},
		{"no shell at all", "", []string{"claude"}},
	} {
		l, err := inShell(c.shell, c.command, home, os.Getenv)
		if err != nil {
			t.Fatalf("%s: %v", c.why, err)
		}
		if !slices.Equal(l.argv, c.command) || len(l.env) != 0 {
			t.Errorf("%s: got %q %q, want the command as is", c.why, l.argv, l.env)
		}
	}
}

func TestInShellQuotesTheCommandForTheShell(t *testing.T) {
	l, err := inShell("/bin/zsh", []string{"claude", "--resume", "it's a b"}, t.TempDir(), os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(l.env, `USH_RUN=claude '--resume' 'it'\''s a b'`) {
		t.Errorf("env %q should carry the command, its arguments quoted", l.env)
	}
}

func TestCommandLineLeavesOnlyAPlainNameBare(t *testing.T) {
	for _, c := range []struct {
		command []string
		want    string
	}{
		{[]string{"claude"}, `claude`},
		{[]string{"npm", "run", "dev"}, `npm 'run' 'dev'`},
		{[]string{"python3.12_x-y", "*"}, `python3.12_x-y '*'`},
		{[]string{"g++", "-O2"}, `'g++' '-O2'`},
		{[]string{"./run.sh", "a b"}, `'./run.sh' 'a b'`},
		{[]string{"my tool"}, `'my tool'`},
		{[]string{"FOO=bar"}, `'FOO=bar'`},
		{[]string{"*"}, `'*'`},
		{[]string{""}, `''`},
	} {
		if got := commandLine(c.command); got != c.want {
			t.Errorf("commandLine(%q) = %s, want %s", c.command, got, c.want)
		}
	}
}

// fakeHome is a $HOME whose rc files change directory, define an alias, set a
// prompt of their own before every prompt and leave a mark, so a test can tell
// the user's rc ran and that ush put the directory back.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	rc := "cd /\nexport RC_RAN=yes\nalias greet='printf \"ALIAS[%s]\\n\"'\n" +
		"precmd() { PS1='MINE> '; }\nPROMPT_COMMAND=precmd\n" // zsh calls precmd itself
	for _, f := range []string{".zshrc", ".bashrc"} {
		if err := os.WriteFile(filepath.Join(home, f), []byte(rc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// script writes an executable that is not a shell (so it gets wrapped).
func script(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// startWrapped runs command inside shell on a pty of its own, as the sharer
// does, from dir, with a fake $HOME.
func startWrapped(t *testing.T, shell string, command []string, dir string) (*exec.Cmd, *os.File) {
	t.Helper()
	if _, err := exec.LookPath(shell); err != nil {
		t.Skipf("%s is not installed", shell)
	}
	home := fakeHome(t)
	env := func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	}
	l, err := inShell(shell, command, home, env)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(l.argv[0], l.argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append([]string{"HOME=" + home, "PATH=/usr/bin:/bin", "TERM=xterm", "PS1=PROMPT> "}, l.env...)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = ptmx.Close()
	})
	return cmd, ptmx
}

func TestTheWrappedCommandRunsAsIfRunDirectly(t *testing.T) {
	probe := script(t, "probe", `printf 'ARGS[%s][%s] PWD[%s] RUN[%s] ZDOTDIR[%s] RC[%s]\n' "$1" "$2" "$PWD" "${USH_RUN-unset}" "${ZDOTDIR-unset}" "${RC_RAN-no}"
exit 7
`)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, shell := range []string{"zsh", "bash"} {
		t.Run(shell, func(t *testing.T) {
			cmd, ptmx := startWrapped(t, shell, []string{probe, "a b", "c'd"}, dir)
			out, _ := io.ReadAll(ptmx) // ends when the shell exits
			err := cmd.Wait()
			want := "ARGS[a b][c'd] PWD[" + dir + "] RUN[unset] ZDOTDIR[unset] RC[yes]"
			if !strings.Contains(string(out), want) {
				t.Errorf("output %q\nwant it to contain %q", out, want)
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 7 {
				t.Errorf("the shell exited %v, want the command's own status 7", err)
			}
		})
	}
}

// An alias from the user's rc runs as the command, as it would at a prompt.
func TestAnAliasRunsAsTheCommand(t *testing.T) {
	for _, shell := range []string{"zsh", "bash"} {
		t.Run(shell, func(t *testing.T) {
			cmd, ptmx := startWrapped(t, shell, []string{"greet", "a b"}, t.TempDir())
			out, _ := io.ReadAll(ptmx) // ends when the shell exits
			if err := cmd.Wait(); err != nil {
				t.Errorf("the shell exited %v, want the alias's own status 0", err)
			}
			if want := "ALIAS[a b]"; !strings.Contains(string(out), want) {
				t.Errorf("output %q\nwant it to contain %q", out, want)
			}
		})
	}
}

// ^Z on the shared command drops to the shell; fg brings it back.
func TestCtrlZDropsToTheShellAndFgComesBack(t *testing.T) {
	sleeper := script(t, "sleeper", "exec sleep 30\n")
	for _, shell := range []string{"zsh", "bash"} {
		t.Run(shell, func(t *testing.T) {
			cmd, ptmx := startWrapped(t, shell, []string{sleeper}, t.TempDir())
			go func() { _, _ = io.Copy(io.Discard, ptmx) }()
			job := waitForChild(t, cmd.Process.Pid)
			waitState(t, job, "S", "running")

			_, _ = ptmx.Write([]byte{0x1a}) // ^Z
			waitState(t, job, "T", "stopped by ^Z")
			if cmd.ProcessState != nil {
				t.Fatal("the shell exited when the command was suspended; it should stay")
			}

			_, _ = ptmx.Write([]byte("fg\r"))
			waitState(t, job, "S", "running again after fg")
		})
	}
}

// Quitting a command that was suspended and brought back ends the shell, with
// the command's own status, as quitting it does when it was never suspended.
func TestQuittingAfterFgEndsTheShell(t *testing.T) {
	waiter := script(t, "waiter", "read line\nexit 7\n")
	for _, shell := range []string{"zsh", "bash"} {
		t.Run(shell, func(t *testing.T) {
			cmd, ptmx := startWrapped(t, shell, []string{waiter}, t.TempDir())
			go func() { _, _ = io.Copy(io.Discard, ptmx) }()
			job := waitForChild(t, cmd.Process.Pid)
			waitState(t, job, "S", "running")

			_, _ = ptmx.Write([]byte{0x1a}) // ^Z
			waitState(t, job, "T", "stopped by ^Z")
			_, _ = ptmx.Write([]byte("fg\r"))
			waitState(t, job, "S", "running again after fg")
			_, _ = ptmx.Write([]byte("quit\r")) // the line the command waits for

			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 7 {
					t.Errorf("the shell exited %v, want the command's own status 7", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the command is gone and the shell is still there")
			}
		})
	}
}

// The first prompt after ^Z is the user's own, not the shell's default.
func TestThePromptAfterCtrlZIsTheUsersOwn(t *testing.T) {
	sleeper := script(t, "sleeper", "exec sleep 30\n")
	for _, shell := range []string{"zsh", "bash"} {
		t.Run(shell, func(t *testing.T) {
			cmd, ptmx := startWrapped(t, shell, []string{sleeper}, t.TempDir())
			var mu sync.Mutex
			var out bytes.Buffer
			go func() {
				b := make([]byte, 4096)
				for {
					n, err := ptmx.Read(b)
					mu.Lock()
					out.Write(b[:n])
					mu.Unlock()
					if err != nil {
						return
					}
				}
			}()
			printed := func() string {
				mu.Lock()
				defer mu.Unlock()
				return out.String()
			}
			job := waitForChild(t, cmd.Process.Pid)
			waitState(t, job, "S", "running")
			_, _ = ptmx.Write([]byte{0x1a}) // ^Z
			waitState(t, job, "T", "stopped by ^Z")

			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) && !strings.Contains(printed(), "> ") {
				time.Sleep(50 * time.Millisecond)
			}
			if got := printed(); !strings.Contains(got, "MINE> ") || strings.Contains(got, "PROMPT> ") {
				t.Errorf("output %q\nwant the prompt the user's rc sets, MINE> ", got)
			}
		})
	}
}

func waitForChild(t *testing.T, parent int) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := exec.Command("pgrep", "-P", strconv.Itoa(parent)).Output()
		if f := strings.Fields(string(out)); len(f) > 0 {
			pid, _ := strconv.Atoi(f[0])
			return pid
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the shell never started the command")
	return 0
}

func waitState(t *testing.T, pid int, want, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		out, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		got = strings.TrimSpace(string(out))
		if strings.HasPrefix(got, want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s: process state %q, want %s", what, got, want)
}
