package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// installed fakes which multiplexers exist on a machine.
func installed(names ...string) func(string) (string, bool) {
	return func(n string) (string, bool) {
		for _, have := range names {
			if have == n {
				return "/usr/bin/" + n, true
			}
		}
		return "", false
	}
}

func TestChooseMux(t *testing.T) {
	cases := []struct {
		name    string
		asked   string
		have    []string
		want    string
		wantErr string
	}{
		{"asked for tmux", "tmux", []string{"tmux", "herdr"}, "tmux", ""},
		{"asked for herdr", "herdr", []string{"tmux", "herdr"}, "herdr", ""},
		{"asked for one that isn't installed", "herdr", []string{"tmux"}, "", "herdr"},
		{"asked for nonsense", "screen", []string{"tmux"}, "", "tmux, herdr"},
		{"asked for ush", "ush", nil, "ush", ""},
		// Not asking means ush's own, which needs nothing installed.
		{"not asked", "", []string{"tmux", "herdr"}, "ush", ""},
		{"not asked, nothing installed", "", nil, "ush", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k, err := chooseMux(c.asked, installed(c.have...))
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("want an error mentioning %q, got %v", c.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if k.name() != c.want {
				t.Fatalf("chose %s, want %s", k.name(), c.want)
			}
		})
	}
}

func TestHerdrRefusesToKeepACommand(t *testing.T) {
	k, err := chooseMux("herdr", installed("herdr"))
	if err != nil {
		t.Fatal(err)
	}
	if k.canKeepCommand() {
		t.Fatal("herdr sessions outlive their command, so it must not keep one")
	}
	if !mustKeeper(t, "tmux").canKeepCommand() {
		t.Fatal("tmux can keep a command")
	}
}

func mustKeeper(t *testing.T, name string) keeper {
	t.Helper()
	k, err := chooseMux(name, installed("tmux", "herdr"))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestHerdrReadsItsSessionList(t *testing.T) {
	const list = `{"sessions":[{"name":"default","running":false},{"name":"api","running":true},{"name":"old","running":false}]}`
	h := herdrKeeper{path: "/usr/bin/herdr", run: func(args ...string) ([]byte, error) { return []byte(list), nil }}
	if !h.exists("api") {
		t.Error("api is running and should count as existing")
	}
	if h.exists("old") {
		t.Error("a stopped session is not a live one")
	}
	if h.exists("nope") {
		t.Error("unknown session")
	}
}

func TestAttachCommands(t *testing.T) {
	tm := tmuxKeeper{path: "/usr/bin/tmux"}
	if got := strings.Join(tm.attachCommand("api"), " "); got != "/usr/bin/tmux attach-session -t =api" {
		t.Errorf("tmux: %s", got)
	}
	h := herdrKeeper{path: "/usr/bin/herdr"}
	if got := strings.Join(h.attachCommand("api"), " "); got != "/usr/bin/herdr --session api" {
		t.Errorf("herdr: %s", got)
	}
}

// Against the real herdr CLI, if it is installed.
func TestHerdrKeeperAgainstRealCLI(t *testing.T) {
	path, err := exec.LookPath("herdr")
	if err != nil {
		t.Skip("herdr not installed")
	}
	// As if run from a herdr pane, which is where herdr users run ush: herdr
	// refuses to start inside itself, so keep mode must leave it first.
	for _, kv := range [][2]string{{"HERDR_ENV", "1"}, {"HERDR_SOCKET_PATH", "/nonexistent/herdr.sock"}, {"HERDR_SESSION", "outer"}} {
		t.Setenv(kv[0], kv[1])
	}
	leaveEnclosingMultiplexer()
	h := herdrKeeper{path: path}
	session := fmt.Sprintf("ush-test-%d", os.Getpid())
	if h.exists(session) {
		t.Fatalf("%s should not exist yet", session)
	}
	t.Cleanup(func() {
		exec.Command(path, "session", "stop", session).Run()
		exec.Command(path, "session", "delete", session).Run()
	})
	// A headless server stands in for the session an attach would start.
	// Setsid keeps herdr off the terminal `go test` was run from.
	ctx, cancel := context.WithCancel(context.Background())
	server := exec.CommandContext(ctx, path, "--session", session, "server")
	server.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		server.Wait()
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !h.exists(session) {
		time.Sleep(200 * time.Millisecond)
	}
	if !h.exists(session) {
		t.Fatal("herdr session did not come up")
	}
}

// Guests get their own multiplexer client, so their terminal size is theirs
// alone: the plain shared pty is sized to the smallest terminal attached to
// it, which let a small viewer shrink the sharer's window.
func TestGuestCommandGivesEachViewerItsOwnClient(t *testing.T) {
	tm := tmuxKeeper{path: "/usr/bin/tmux"}
	want := []string{"/usr/bin/tmux", "attach-session", "-t", "=api"}
	if got := tm.guestCommand("api", false); !slices.Equal(got, want) {
		t.Errorf("tmux guest command: %q, want %q", got, want)
	}
	wantRO := []string{"/usr/bin/tmux", "attach-session", "-r", "-t", "=api"}
	if got := tm.guestCommand("api", true); !slices.Equal(got, wantRO) {
		t.Errorf("read-only guest command: %q, want %q", got, wantRO)
	}
	if got := (tmuxKeeper{path: "/usr/bin/tmux", socket: "ush"}).guestCommand("api", false); !slices.Contains(got, "-L") {
		t.Errorf("guest command must use the same socket as the sharer: %q", got)
	}
	wantHerdr := []string{"/usr/bin/herdr", "session", "attach", "api"}
	if got := (herdrKeeper{path: "/usr/bin/herdr"}).guestCommand("api", false); !slices.Equal(got, wantHerdr) {
		t.Errorf("herdr guest command: %q, want %q", got, wantHerdr)
	}
	// herdr has no read-only client; a read-only session discards guest input instead.
	if got := (herdrKeeper{path: "/usr/bin/herdr"}).guestCommand("api", true); !slices.Equal(got, wantHerdr) {
		t.Errorf("read-only herdr guest command: %q, want %q", got, wantHerdr)
	}
}

// ush keeps the session itself, so the guest command is another
// attachment of ours: that is what gives each viewer a live screen at its own
// size, without a multiplexer in the middle.
func TestUshKeeperIsSelfContained(t *testing.T) {
	u := ushKeeper{path: "/usr/local/bin/ush"}
	if got := u.attachCommand("api"); !slices.Equal(got, []string{"/usr/local/bin/ush", "attach", "api"}) {
		t.Errorf("attach command: %q", got)
	}
	if got := u.guestCommand("api", false); !slices.Equal(got, []string{"/usr/local/bin/ush", "attach", "api"}) {
		t.Errorf("guest command: %q", got)
	}
	// The flag before the name, or ush attach stops parsing at the name.
	if got := u.guestCommand("api", true); !slices.Equal(got, []string{"/usr/local/bin/ush", "attach", "--read-only", "api"}) {
		t.Errorf("a read-only share must attach read-only: %q", got)
	}
	if !u.canKeepCommand() {
		t.Error("a kept session ends with its command, so ush can keep one")
	}
	if got := u.reattachHint("api"); got != "ush attach api" {
		t.Errorf("hint: %q", got)
	}
}

func TestChoosingUshNeedsNothingInstalled(t *testing.T) {
	// The other backends must be on PATH; this one is the running binary.
	k, err := chooseMux("ush", installed())
	if err != nil {
		t.Fatalf("--mux ush should not need anything installed: %v", err)
	}
	if k.name() != "ush" {
		t.Fatalf("chose %q", k.name())
	}
}

func TestReattachHintMatchesTheBackend(t *testing.T) {
	if got := (tmuxKeeper{path: "/usr/bin/tmux"}).reattachHint("api"); got != "tmux attach -t api" {
		t.Errorf("tmux hint: %q", got)
	}
	if got := (herdrKeeper{path: "/usr/bin/herdr"}).reattachHint("api"); got != "herdr --session api" {
		t.Errorf("herdr hint: %q", got)
	}
}

// What each backend refuses, checked before anything is started.
func TestKeepRefusals(t *testing.T) {
	cases := []struct {
		tm      keeper
		o       options
		wantErr string
	}{
		{herdrKeeper{}, options{keepCommand: true, name: "api"}, "herdr"},
		{tmuxKeeper{}, options{name: "api.v2"}, "can't contain"},
		{herdrKeeper{}, options{name: "a:b"}, "can't contain"},
		{ushKeeper{}, options{keepCommand: true, name: "api.v2"}, ""},
		{tmuxKeeper{}, options{keepCommand: true, name: "api"}, ""},
	}
	for _, c := range cases {
		err := keepRefusal(c.tm, c.o)
		if c.wantErr == "" && err != nil {
			t.Errorf("%s %+v: refused: %v", c.tm.name(), c.o, err)
		}
		if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%s %+v: want an error mentioning %q, got %v", c.tm.name(), c.o, c.wantErr, err)
		}
	}
}

func TestLeavingTheEnclosingMultiplexer(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,1,0")
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "p1")
	t.Setenv("HERDRX", "not herdr's") // only HERDR_* is herdr's
	leaveEnclosingMultiplexer()
	for _, k := range []string{"TMUX", "HERDR_ENV", "HERDR_PANE_ID"} {
		if v, set := os.LookupEnv(k); set {
			t.Errorf("%s=%q is still set", k, v)
		}
	}
	if os.Getenv("HERDRX") != "not herdr's" {
		t.Error("a variable that only looks like herdr's was removed")
	}
}
