package rendezvous

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

var fixed = time.Unix(1_800_000_000, 0)

func newTestMenu(t *testing.T, sessions ...session) (*menu, string) {
	t.Helper()
	dir := t.TempDir()
	for _, s := range sessions {
		writeSession(t, dir, s)
	}
	m := newMenu(menuConfig{dir: dir, now: func() time.Time { return fixed }})
	m.reload()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return m, dir
}

func typeText(m *menu, s string) {
	for _, r := range s {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

var (
	api   = session{ID: "AAAAAAAAAAAAAAAAAAAA", Name: "api", Machine: "lumen", Command: "claude", Cwd: "/src/api", User: "alex", StartedEpoch: fixed.Unix() - 90, Started: "2027-01-15T08:00:00Z"}
	build = session{ID: "BBBBBBBBBBBBBBBBBBBB", Name: "build", Machine: "umber", Command: "make watch", StartedEpoch: fixed.Unix() - 30}
)

func TestListShowsSessionsWithAges(t *testing.T) {
	m, _ := newTestMenu(t, api, build)
	v := m.View()
	for _, want := range []string{"api", "lumen", "claude", "1m", "build", "umber", "make watch", "30s"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q", want)
		}
	}
}

func TestTypingFiltersFuzzily(t *testing.T) {
	m, _ := newTestMenu(t, api, build)
	typeText(m, "bld")
	if got := m.selected(); got == nil || got.Name != "build" {
		t.Fatalf("fuzzy 'bld' should select build, got %+v", got)
	}
	if strings.Contains(listPart(m.View()), "claude") {
		t.Error("api should be filtered out of the list")
	}
}

func TestRefreshKeepsTheCursorOnTheSameSession(t *testing.T) {
	m, dir := newTestMenu(t, api, build)
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.selected().Name != "build" {
		t.Fatal("setup: expected build selected")
	}
	// A new session that sorts first would shift rows; the cursor must follow build.
	writeSession(t, dir, session{ID: "CCCCCCCCCCCCCCCCCCCC", Name: "aardvark", StartedEpoch: fixed.Unix()})
	m.Update(tickMsg{})
	if got := m.selected(); got == nil || got.Name != "build" {
		t.Fatalf("cursor moved off build after refresh: %+v", got)
	}
	if !strings.Contains(m.View(), "aardvark") {
		t.Error("new session did not appear")
	}
}

func TestRefreshKeepsTheFilter(t *testing.T) {
	m, dir := newTestMenu(t, api, build)
	typeText(m, "bui")
	writeSession(t, dir, session{ID: "CCCCCCCCCCCCCCCCCCCC", Name: "builder", StartedEpoch: fixed.Unix()})
	m.Update(tickMsg{})
	if m.query.Value() != "bui" {
		t.Fatalf("filter changed to %q", m.query.Value())
	}
	if !strings.Contains(listPart(m.View()), "builder") || strings.Contains(listPart(m.View()), "claude") {
		t.Error("refreshed list should still be filtered")
	}
}

func TestEscClearsFilterThenQuits(t *testing.T) {
	m, _ := newTestMenu(t, api)
	typeText(m, "x")
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc}); isQuit(cmd) || m.query.Value() != "" {
		t.Fatal("first esc should clear the filter, not quit")
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc}); !isQuit(cmd) {
		t.Fatal("second esc should quit")
	}
}

func TestEmptyState(t *testing.T) {
	m, _ := newTestMenu(t)
	if !strings.Contains(m.View(), "No active sessions") {
		t.Fatalf("empty view: %q", m.View())
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}); !isQuit(cmd) {
		t.Fatal("q should quit the empty menu")
	}
}

func TestNotices(t *testing.T) {
	m, _ := newTestMenu(t, api)
	for _, c := range []struct {
		msg  joinedMsg
		want string
	}{
		{joinedMsg{name: "api", outcome: detached}, `left "api", still running`},
		{joinedMsg{name: "api", outcome: ended, gone: true}, `session "api" ended`},
		{joinedMsg{name: "api", outcome: ended, gone: false}, `lost connection to "api", still running`},
		{joinedMsg{name: "api", outcome: failed, err: errString("boom")}, `could not join "api": boom`},
	} {
		m.Update(c.msg)
		if !strings.Contains(m.View(), c.want) {
			t.Errorf("after %+v, view lacks %q", c.msg, c.want)
		}
	}
}

func TestCardIsReadableNotJSON(t *testing.T) {
	c := card(api, fixed, 50)
	for _, want := range []string{"api", "machine", "lumen", "command", "claude", "cwd", "/src/api", "access", "can type", "started", "1m ago"} {
		if !strings.Contains(c, want) {
			t.Errorf("card missing %q:\n%s", want, c)
		}
	}
	if strings.Contains(c, `"session_id"`) || strings.Contains(c, "{") {
		t.Errorf("card looks like JSON:\n%s", c)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// listPart is the view without the detail card, so filter assertions aren't
// fooled by the card of the selected session.
func listPart(view string) string {
	var b strings.Builder
	for _, line := range strings.Split(view, "\n") {
		if i := strings.Index(line, "│"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func TestFilterAfterJoin(t *testing.T) {
	m, _ := newTestMenu(t, api, build)
	typeText(m, "api")
	m.Update(joinedMsg{name: "api", outcome: detached})
	if m.query.Value() != "api" {
		t.Fatal("after detaching, the filter should stay for an easy rejoin")
	}
	m.Update(joinedMsg{name: "api", outcome: ended, gone: true})
	if m.query.Value() != "" {
		t.Fatal("after the session ended, its filter would hide everything else; clear it")
	}
}

func TestViewNeverOutgrowsTheScreen(t *testing.T) {
	// The card is taller than a small window gives it. Anything past the
	// last row scrolls the top away: the help, the header, then the list.
	tall := api
	tall.Description = "a description long enough to wrap onto more than one line of the card"
	for _, size := range []tea.WindowSizeMsg{{Width: 80, Height: 24}, {Width: 80, Height: 15}, {Width: 60, Height: 12}, {Width: 100, Height: 10}} {
		m, _ := newTestMenu(t, tall, build)
		m.Update(size)
		view := m.View()
		lines := strings.Split(view, "\n")
		if len(lines) != size.Height {
			t.Errorf("%dx%d: view is %d lines, want exactly %d", size.Width, size.Height, len(lines), size.Height)
		}
		for _, want := range []string{"enter joins", "NAME", "lumen", "api"} {
			if !strings.Contains(strings.Join(lines[:min(len(lines), 4)], "\n"), want) {
				t.Errorf("%dx%d: %q should be in the first lines:\n%s", size.Width, size.Height, want, view)
			}
		}
	}
}

func TestViewFillsTheScreen(t *testing.T) {
	m, _ := newTestMenu(t, api, build)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 30 {
		t.Fatalf("view is %d lines, want the full 30", len(lines))
	}
	if !strings.Contains(lines[len(lines)-1], "/") { // the "N/M" counter rides with the filter
		t.Fatalf("filter line should be the last line, got %q", lines[len(lines)-1])
	}
	// The divider between list and card runs the whole way down.
	dividers := 0
	for _, l := range lines {
		if strings.Contains(l, "│") {
			dividers++
		}
	}
	if dividers < 20 {
		t.Fatalf("divider only on %d lines; it should run the full height", dividers)
	}
}

func TestListIsATreeOfMachines(t *testing.T) {
	// aaa sorts first by name but lives on umber, so it belongs under
	// umber, after lumen's sessions.
	aaa := session{ID: "CCCCCCCCCCCCCCCCCCCC", Name: "aaa", Machine: "umber", Command: "top", StartedEpoch: fixed.Unix()}
	m, _ := newTestMenu(t, api, build, aaa)
	var rows []string
	for _, l := range strings.Split(listPart(m.View()), "\n") {
		if f := strings.Fields(strings.Trim(l, "▌ ")); len(f) > 0 {
			rows = append(rows, strings.Join(f[:min(len(f), 2)], " "))
		}
	}
	want := []string{"NAME COMMAND", "lumen", "└ api", "umber", "├ aaa", "└ build"}
	if !slices.Equal(rows[1:7], want) {
		t.Fatalf("tree rows = %q, want %q", rows[1:7], want)
	}
	// The cursor walks the sessions, never the machine headings.
	for _, name := range []string{"api", "aaa", "build", "build"} {
		if got := m.selected().Name; got != name {
			t.Fatalf("cursor on %q, want %q", got, name)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
}

func TestFilterSelectsTheBestMatchWhereverItSits(t *testing.T) {
	// Both match, build best, but b_u_i_l_d's machine heads the tree.
	worse := session{ID: "CCCCCCCCCCCCCCCCCCCC", Name: "b_u_i_l_d", Machine: "aaa-box", Command: "sleep", StartedEpoch: fixed.Unix()}
	m, _ := newTestMenu(t, build, worse)
	typeText(m, "build")
	if got := m.selected(); got == nil || got.Name != "build" {
		t.Fatalf("best match is build, got %+v", got)
	}
	if m.cursor != 1 {
		t.Fatalf("build sits under umber, after aaa-box: cursor %d, want 1", m.cursor)
	}
}

func TestMachineColorsMatchTheIOSApp(t *testing.T) {
	// Indices from the Swift colorIndex(machine:count:) for the same names.
	for machine, want := range map[string]int{"lumen": 2, "umber": 4, "laptop": 5, "büro": 5} {
		if got := machineColor(machine); got != machinePalette[want] {
			t.Errorf("%s: got %s, want palette[%d] %s", machine, got, want, machinePalette[want])
		}
	}
}

func TestListCountsViewers(t *testing.T) {
	dir, viewers := t.TempDir(), t.TempDir()
	writeSession(t, dir, api)
	writeSession(t, dir, build)
	// Two live processes watching api; nobody on build.
	os.MkdirAll(filepath.Join(viewers, api.ID), 0o755)
	for _, pid := range []int{os.Getpid(), os.Getppid()} {
		os.WriteFile(filepath.Join(viewers, api.ID, strconv.Itoa(pid)), nil, 0o644)
	}
	m := newMenu(menuConfig{dir: dir, viewers: viewers, now: func() time.Time { return fixed }})
	m.reload()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	list := listPart(m.View())
	if !strings.Contains(list, "VIEWERS") {
		t.Errorf("no VIEWERS column:\n%s", list)
	}
	if !regexp.MustCompile(`claude\s+2\s+1m`).MatchString(list) {
		t.Errorf("api should show 2 viewers:\n%s", list)
	}
	if !regexp.MustCompile(`make watch\s+30s`).MatchString(list) {
		t.Errorf("build should show no count:\n%s", list)
	}
}

func TestCardSaysWhoIsWatching(t *testing.T) {
	watched := api
	watched.Viewers = 2
	if c := card(watched, fixed, 50); !strings.Contains(c, "2 joined") {
		t.Errorf("card missing the viewer count:\n%s", c)
	}
	if c := card(build, fixed, 50); !strings.Contains(c, "nobody") {
		t.Errorf("card should say nobody is watching:\n%s", c)
	}
}
