package rendezvous

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func key(s string) tea.KeyMsg {
	switch s {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func newTestPanel(rename func(string) error) *panel {
	joined := fixed.Add(-12 * time.Minute)
	return newPanel(panelInfo{
		session: session{Name: "api", Machine: "lumen", Command: "claude", StartedEpoch: fixed.Add(-3*time.Hour - 4*time.Minute).Unix()},
		viewers: 2,
		joined:  joined,
	}, rename, func() time.Time { return fixed })
}

func TestPanelShowsTheSession(t *testing.T) {
	v := newTestPanel(nil).View()
	for _, want := range []string{"api", "lumen", "claude", "2 (incl. you)", "12m", "3h 04m",
		"back to session", "back to list", "refresh", "rename", "d/q  disconnect"} {
		if !strings.Contains(v, want) {
			t.Errorf("panel missing %q:\n%s", want, v)
		}
	}
}

func TestPanelShortcuts(t *testing.T) {
	for k, want := range map[string]panelChoice{"b": choiceBack, "esc": choiceBack, "l": choiceList,
		"d": choiceDisconnect, "q": choiceDisconnect, "r": choiceRedraw} {
		p := newTestPanel(nil)
		if _, cmd := p.Update(key(k)); !isQuit(cmd) || p.choice != want {
			t.Errorf("%s: choice %v quit=%v, want %v", k, p.choice, isQuit(cmd), want)
		}
	}
}

func TestPanelArrowsAndEnter(t *testing.T) {
	p := newTestPanel(nil)
	p.Update(key("down")) // refresh
	p.Update(key("down")) // back to list
	if _, cmd := p.Update(key("enter")); !isQuit(cmd) || p.choice != choiceList {
		t.Fatalf("down+enter: %v", p.choice)
	}
	// Every item does what its key does when chosen with enter.
	p = newTestPanel(nil)
	for range panelItems {
		p.Update(key("down"))
	}
	if _, cmd := p.Update(key("enter")); !isQuit(cmd) || p.choice != choiceDisconnect {
		t.Fatalf("enter on the last item: %v", p.choice)
	}
}

func TestPanelRename(t *testing.T) {
	var got string
	p := newTestPanel(func(n string) error { got = n; return nil })
	p.Update(key("n"))
	for _, r := range "api-v2" {
		p.Update(key(string(r)))
	}
	if _, cmd := p.Update(key("enter")); isQuit(cmd) {
		t.Fatal("renaming should stay in the panel")
	}
	if got != "api-v2" || !strings.Contains(p.View(), "api-v2") {
		t.Fatalf("rename not applied: got %q\n%s", got, p.View())
	}
}

func TestPanelRenameRejectsBadNames(t *testing.T) {
	called := false
	p := newTestPanel(func(string) error { called = true; return nil })
	p.Update(key("n"))
	for _, r := range "has space" {
		p.Update(key(string(r)))
	}
	p.Update(key("enter"))
	if called || !strings.Contains(p.View(), "letters, digits") {
		t.Fatalf("bad name should be refused with a hint:\n%s", p.View())
	}
	if _, cmd := p.Update(key("esc")); isQuit(cmd) {
		t.Fatal("esc while renaming should cancel the rename, not leave the panel")
	}
}

func TestRenameSessionRewritesTheRegistry(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, api)
	if err := renameSession(dir, api.ID, "api-v2"); err != nil {
		t.Fatal(err)
	}
	if got := loadSessions(dir); len(got) != 1 || got[0].Name != "api-v2" || got[0].Machine != "lumen" {
		t.Fatalf("registry after rename: %+v", got)
	}
	if err := renameSession(dir, "NOSUCHSESSION0000000", "x"); err == nil {
		t.Fatal("renaming a vanished session should fail")
	}
}

func TestViewerCount(t *testing.T) {
	dir := t.TempDir()
	leave, err := markViewer(dir, api.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A marker from a process that no longer exists must not count.
	stale := filepath.Join(dir, api.ID, strconv.Itoa(999999))
	os.WriteFile(stale, nil, 0o644)
	if n := countViewers(dir, api.ID); n != 1 {
		t.Fatalf("want 1 live viewer, got %d", n)
	}
	leave()
	if n := countViewers(dir, api.ID); n != 0 {
		t.Fatalf("want 0 after leaving, got %d", n)
	}
}

func TestPausableBuffersThenReplays(t *testing.T) {
	var out bytes.Buffer
	p := newPausable(&out, 1024)
	p.Write([]byte("a"))
	p.pause()
	p.Write([]byte("b"))
	p.Write([]byte("c"))
	if out.String() != "a" {
		t.Fatalf("paused output leaked: %q", out.String())
	}
	if overflowed := p.resume(); overflowed || out.String() != "abc" {
		t.Fatalf("after resume %q overflowed=%v", out.String(), overflowed)
	}
}

func TestPausableReportsOverflow(t *testing.T) {
	var out bytes.Buffer
	p := newPausable(&out, 4)
	p.pause()
	p.Write([]byte("0123456789"))
	if !p.resume() {
		t.Fatal("want overflow reported so the caller can force a redraw")
	}
}

func TestPanelIsCentered(t *testing.T) {
	p := newTestPanel(nil)
	p.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	lines := strings.Split(p.View(), "\n")
	top := 0
	for top < len(lines) && strings.TrimSpace(lines[top]) == "" {
		top++
	}
	if top == 0 || top >= len(lines) {
		t.Fatalf("box should be pushed down from the top, starts at line %d", top)
	}
	indent := len(lines[top]) - len(strings.TrimLeft(lines[top], " "))
	if indent < 20 {
		t.Fatalf("box should be pushed in from the left, indent %d", indent)
	}
	// Roughly centered: as much space below the box as above it.
	bottom := 0
	for i := len(lines) - 1; i >= 0 && strings.TrimSpace(lines[i]) == ""; i-- {
		bottom++
	}
	if diff := top - bottom; diff < -1 || diff > 1 {
		t.Fatalf("not vertically centered: %d blank lines above, %d below", top, bottom)
	}
}
