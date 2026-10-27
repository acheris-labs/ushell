package main

import (
	"ush/internal/config"

	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	var b backoff
	var got []time.Duration
	for range 7 {
		got = append(got, b.next(time.Second))
	}
	want := []time.Duration{2, 4, 8, 16, 30, 30, 30}
	for i := range want {
		if got[i] != want[i]*time.Second {
			t.Fatalf("waits %v, want %v seconds", got, want)
		}
	}
	// A connection that held for a while starts the backoff over.
	if w := b.next(5 * time.Minute); w != 2*time.Second {
		t.Fatalf("after a long-lived connection, want 2s, got %v", w)
	}
}

// privateTmux uses its own tmux server, so tests never touch the user's.
func privateTmux(t *testing.T) tmuxKeeper {
	t.Helper()
	path, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	tm := tmuxKeeper{path: path}
	tm.socket = fmt.Sprintf("ush-test-%d", os.Getpid())
	t.Cleanup(func() { exec.Command(tm.path, "-L", tm.socket, "kill-server").Run() })
	return tm
}

func TestTmuxSessionLifecycle(t *testing.T) {
	tm := privateTmux(t)
	if tm.exists("keeper") {
		t.Fatal("fresh server should have no sessions")
	}
	if err := tm.create(options{name: "keeper", command: []string{"sleep", "300"}}); err != nil {
		t.Fatal(err)
	}
	if !tm.exists("keeper") {
		t.Fatal("session not created")
	}
	// The command runs as a job of $SHELL, which starts it a moment later.
	deadline := time.Now().Add(5 * time.Second)
	for tm.currentCommand("keeper") != "sleep" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := tm.currentCommand("keeper"); got != "sleep" {
		t.Fatalf("running %q, want sleep", got)
	}
	env, _ := exec.Command(tm.path, "-L", tm.socket, "show-environment", "-t", "=keeper", "USH_SESSION").Output()
	if strings.TrimSpace(string(env)) != "USH_SESSION=keeper" {
		t.Fatalf("session environment: %q", env)
	}
	size, _ := exec.Command(tm.path, "-L", tm.socket, "show-options", "-w", "-t", "=keeper:", "-v", "window-size").Output()
	if strings.TrimSpace(string(size)) != "latest" {
		t.Fatalf("window-size %q, want latest so a small viewer can't shrink it", size)
	}
}

func TestTmuxNamesMatchExactly(t *testing.T) {
	// tmux targets match prefixes by default: "api" would find "api-v2".
	tm := privateTmux(t)
	tm.create(options{name: "api-v2", command: []string{"sleep", "300"}})
	if tm.exists("api") {
		t.Fatal(`"api" must not match the session "api-v2"`)
	}
}

func TestAttachCommandTargetsExactly(t *testing.T) {
	tm := tmuxKeeper{path: "/usr/bin/tmux"}
	if got := strings.Join(tm.attachCommand("api"), " "); got != "/usr/bin/tmux attach-session -t =api" {
		t.Fatalf("got %q", got)
	}
}

func TestKeepRefusesAMuxThatIsNotInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no tmux anywhere
	err := keepSharing(config.Config{}, options{share: true, keep: true, mux: "tmux", name: "api", command: []string{"bash"}}, "")
	if err == nil || !strings.Contains(err.Error(), "tmux isn't installed") {
		t.Fatalf("want a refusal naming tmux, got %v", err)
	}
}

// A name that is already running something else is a collision, not an
// invitation: sharing it would hand out a session nobody asked for.
func TestACollidingNameIsRefused(t *testing.T) {
	busy := fakeKeeper{there: true, running: "vim"}
	err := prepare(busy, options{keep: true, keepCommand: true, name: "api", command: []string{"claude"}})
	if err == nil {
		t.Fatal("sharing over a session running something else must fail")
	}
	for _, want := range []string{"already exists", "vim", "another name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

// A kept command that was suspended leaves its $SHELL in front: still the
// same session, so still a reattach -- unless the shell was turned off.
func TestASuspendedCommandStillReattaches(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	suspended := fakeKeeper{there: true, running: "zsh"}
	o := options{keep: true, keepCommand: true, name: "api", command: []string{"claude"}}
	if err := prepare(suspended, o); err != nil {
		t.Fatalf("a suspended command should still reattach: %v", err)
	}
	o.noShell = true
	if err := prepare(suspended, o); err == nil {
		t.Fatal("with --no-shell a shell there is something else, and must be refused")
	}
}

// Asking for the same command that is already running is a reattach, which is
// the whole point of keep mode.
func TestTheSameCommandReattaches(t *testing.T) {
	same := fakeKeeper{there: true, running: "claude"}
	if err := prepare(same, options{keep: true, keepCommand: true, name: "api", command: []string{"claude"}}); err != nil {
		t.Fatalf("reattaching to the same command should be fine: %v", err)
	}
	// And -k, which asks for a shell, reattaches to whatever is running.
	if err := prepare(fakeKeeper{there: true, running: "vim"}, options{keep: true, name: "api"}); err != nil {
		t.Fatalf("-k should reattach to an existing session: %v", err)
	}
}

// fakeKeeper stands in for a multiplexer: it never starts anything.
type fakeKeeper struct {
	there   bool
	running string
}

func (fakeKeeper) name() string                       { return "fake" }
func (f fakeKeeper) exists(string) bool               { return f.there }
func (fakeKeeper) create(options) error               { return nil }
func (fakeKeeper) sharesItself() bool                 { return false }
func (fakeKeeper) attachCommand(string) []string      { return []string{"true"} }
func (fakeKeeper) guestCommand(string, bool) []string { return nil }
func (fakeKeeper) canKeepCommand() bool               { return true }
func (f fakeKeeper) currentCommand(string) string     { return f.running }
func (fakeKeeper) reattachHint(s string) string       { return "fake attach " + s }
