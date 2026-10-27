package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// TestShareErrorsNameTheRendezvous checks the exact messages shareError
// builds from what register.Register actually returns (see
// internal/register/register.go), not from upterm's old "ssh dial error:
// ..." shape.
func TestShareErrorsNameTheRendezvous(t *testing.T) {
	const (
		addr = "10.0.0.1:7874"
		fp   = "SHA256:deadbeef"
	)
	cases := map[string]struct {
		err  error
		want string
	}{
		// register.open wraps a dial failure as "connecting to rendezvous
		// ADDR: %w"; explain must still recognise the *net.OpError
		// underneath, and the address must survive into the final message.
		"unreachable": {
			fmt.Errorf("connecting to rendezvous %s: %w", addr, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}),
			"rendezvous 10.0.0.1:7874: unreachable (connection refused): wrong host or port, the rendezvous is down, or the port is not exposed",
		},
		// register.open wraps a handshake failure as "authenticating to
		// rendezvous ADDR: %w"; the message must name both the rendezvous
		// and the key's fingerprint, since share never prints one "above"
		// the way ush ping does.
		"key rejected": {
			fmt.Errorf("authenticating to rendezvous %s: %w", addr,
				errors.New("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey], no supported methods remain")),
			"rendezvous 10.0.0.1:7874: your key (SHA256:deadbeef) was rejected: it is not the rendezvous's key; check that fingerprint against the rendezvous's log",
		},
		// rejection() in register.go returns this literally when the
		// rendezvous predates the `share` command. It already names itself,
		// so it is left alone: "rendezvous 10.0.0.1:7874: the rendezvous is
		// older..." would repeat "rendezvous" as its very next word.
		"redeploy": {
			errors.New("the rendezvous is older than this ush: redeploy it"),
			"the rendezvous is older than this ush: redeploy it",
		},
		// rejection() in register.go returns this when the rendezvous itself
		// refused the registration. Also left alone, for the same reason.
		"rejected registration": {
			errors.New("rendezvous rejected registration: bad name"),
			"rendezvous rejected registration: bad name",
		},
		// An error explain can't say anything special about, and that isn't
		// one of register.go's own self-naming rejections, still needs the
		// address: nothing else says which rendezvous failed.
		"unrecognised": {
			errors.New("opening registration channel: boom"),
			"rendezvous 10.0.0.1:7874: opening registration channel: boom",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := shareError(addr, fp, c.err).Error(); got != c.want {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestShareErrorPassesNilThrough(t *testing.T) {
	if err := shareError("10.0.0.1:7874", "SHA256:deadbeef", nil); err != nil {
		t.Fatalf("got %v, want nil", err)
	}
}

// The log lives where only this user can reach it, and a symlink planted in
// its place is not followed.
func TestLogIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home", ".ush")
	path := filepath.Join(dir, "ush.log")
	f, err := openLog(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	for p, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: want mode %v (%v)", p, want, err)
		}
	}

	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if f, err := openLog(path); err == nil {
		f.Close()
		t.Fatal("a symlinked log should not be opened")
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("the symlink's target was created")
	}
}

// Every share appends to the one log. Each opens it for itself, as separate
// processes do, and none may overwrite what is there or splice its lines into
// another's.
func TestSharesShareTheLogWithoutClobbering(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".ush", "ush.log")
	f, err := openLog(path)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, "earlier line")
	f.Close()

	const shares, lines = 8, 500
	var wg sync.WaitGroup
	for i := 0; i < shares; i++ {
		f, err := openLog(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		logger := slog.New(slog.NewTextHandler(f, nil)).With("session", fmt.Sprintf("share%d", i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < lines; n++ {
				logger.Info("line", "n", n, "pad", strings.Repeat("x", 200))
			}
		}()
	}
	wg.Wait()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if got[0] != "earlier line" {
		t.Fatalf("what was there before was overwritten: first line %q", got[0])
	}
	per := map[string]int{}
	for _, l := range got[1:] {
		if strings.Count(l, "time=") != 1 || !strings.HasSuffix(l, strings.Repeat("x", 200)) {
			t.Fatalf("a line was split or spliced: %q", l)
		}
		per[l[strings.Index(l, "session=")+len("session="):][:6]]++
	}
	for i := 0; i < shares; i++ {
		if n := per[fmt.Sprintf("share%d", i)]; n != lines {
			t.Errorf("share%d: %d lines, want %d", i, n, lines)
		}
	}
}
