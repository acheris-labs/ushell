package register

import (
	"encoding/base64"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ptr(s string) *string { return &s }

func TestSetChecksAndLeavesNilFieldsAlone(t *testing.T) {
	l := NewLabel("api", "old")
	if err := l.Set(nil, ptr("new")); err != nil {
		t.Fatal(err)
	}
	if n, d := l.Get(); n != "api" || d != "new" {
		t.Fatalf("got %q, %q; want the name kept", n, d)
	}
	for _, bad := range []struct{ name, desc *string }{
		{ptr("-flag"), nil},
		{ptr("has space"), nil},
		{ptr(strings.Repeat("a", 33)), nil},
		{nil, ptr("café")},
		{nil, ptr("tab\there")},
		{nil, ptr(strings.Repeat("x", MaxDescription+1))},
	} {
		if err := l.Set(bad.name, bad.desc); err == nil {
			t.Errorf("Set(%v, %v) was accepted", bad.name, bad.desc)
		}
	}
	if n, d := l.Get(); n != "api" || d != "new" {
		t.Fatalf("a refused Set changed the label to %q, %q", n, d)
	}
}

func TestUpdatesRoundTrip(t *testing.T) {
	n, d, ok := parseUpdate(updateLine("api", "has = and spaces"))
	if !ok || n != "api" || d != "has = and spaces" {
		t.Fatalf("got %q, %q, %v", n, d, ok)
	}
	for _, line := range []string{"registered api (x)\n", "update not-base64!\n", updateLine("", "x"), ""} {
		if _, _, ok := parseUpdate(line); ok {
			t.Errorf("parseUpdate(%q) accepted it", line)
		}
	}
}

func socketDir(t *testing.T) string {
	t.Helper()
	// A unix socket path is capped near 104 bytes on macOS, past t.TempDir().
	dir, err := os.MkdirTemp("", "ushctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestTheControlSocketGetsAndSets(t *testing.T) {
	path := ControlPath(socketDir(t), "api")
	l := NewLabel("api", "")
	ln, err := ServeControl(path, l)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	if n, d, err := GetLabel(path); err != nil || n != "api" || d != "" {
		t.Fatalf("get: %q, %q, %v", n, d, err)
	}
	if n, d, err := SetLabel(path, nil, ptr("pr monitor")); err != nil || n != "api" || d != "pr monitor" {
		t.Fatalf("set description: %q, %q, %v", n, d, err)
	}
	if n, d, err := SetLabel(path, ptr("api-v2"), nil); err != nil || n != "api-v2" || d != "pr monitor" {
		t.Fatalf("set name: %q, %q, %v", n, d, err)
	}
	if _, _, err := SetLabel(path, ptr("bad name"), nil); err == nil || !strings.Contains(err.Error(), "1-32") {
		t.Fatalf("a bad name should come back with the reason, got %v", err)
	}
	select {
	case <-l.unsent:
	default:
		t.Fatal("an edit made here should be waiting to go to the rendezvous")
	}
}

func TestTheControlSocketIsNotStolen(t *testing.T) {
	path := ControlPath(socketDir(t), "api")
	ln, err := ServeControl(path, NewLabel("api", ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ServeControl(path, NewLabel("api", "")); err == nil {
		t.Fatal("a second share took over a socket the first still answers on")
	}
	ln.Close()

	// One left behind by a share that died is taken over.
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the stale socket should still be there: %v", err)
	}
	ln, err = ServeControl(path, NewLabel("api", ""))
	if err != nil {
		t.Fatalf("a stale socket should be replaced: %v", err)
	}
	ln.Close()
}

func TestNoShareListening(t *testing.T) {
	if _, _, err := GetLabel(filepath.Join(socketDir(t), "none.ctl")); err == nil ||
		!strings.Contains(err.Error(), "no share is listening") {
		t.Fatalf("got %v", err)
	}
}

func TestAnEditWaitingToGoOutranksTheRendezvous(t *testing.T) {
	l := NewLabel("api", "")
	if _, _, changed := l.apply(updateLine("api", "from the menu")); !changed {
		t.Fatal("an update from the rendezvous should apply")
	}
	if _, _, changed := l.apply(updateLine("api", "from the menu")); changed {
		t.Fatal("the same label again is not a change")
	}
	if err := l.Set(nil, ptr("typed here")); err != nil {
		t.Fatal(err)
	}
	if _, _, changed := l.apply(updateLine("api", "crossed in the post")); changed {
		t.Fatal("an update that crossed an unsent edit replaced it")
	}
	if _, d := l.Get(); d != "typed here" {
		t.Fatalf("description is %q", d)
	}
	if line := l.edits(); line != updateLineOf("description=typed here\n") {
		t.Fatalf("sent %q; want only the description, the one field edited", line)
	}
	if l.edits() != "" {
		t.Fatal("an edit is sent once")
	}
	if _, d, _ := l.apply(updateLine("api", "typed here, as saved")); d != "typed here, as saved" {
		t.Fatalf("after sending, the saved label should apply; got %q", d)
	}
}

func updateLineOf(raw string) string {
	return "update " + base64.StdEncoding.EncodeToString([]byte(raw)) + "\n"
}

func TestARenameHereDoesNotResendTheDescription(t *testing.T) {
	l := NewLabel("api", "old")
	l.apply(updateLine("api", "set from the menu")) // the menu's edit reached us
	if err := l.Set(ptr("api-v2"), nil); err != nil {
		t.Fatal(err)
	}
	if line := l.edits(); line != updateLineOf("name=api-v2\n") {
		t.Fatalf("sent %q; want only the name", line)
	}
	// A registration carries the whole label, so nothing is left to send.
	_ = l.Set(nil, ptr("x"))
	if n, d := l.registering(); n != "api-v2" || d != "x" || l.edits() != "" {
		t.Fatalf("registering gave %q, %q and left edits to send", n, d)
	}
}
