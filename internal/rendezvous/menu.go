package rendezvous

import (
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/sahilm/fuzzy"
	"golang.org/x/term"
)

const refreshEvery = 2 * time.Second

type menuConfig struct {
	dir     string               // the session registry
	viewers string               // where joined viewers are marked
	join    func(session) target // how to reach a session; nil in tests
	now     func() time.Time
}

type tickMsg struct{}

// joinedMsg reports how a join from the menu went.
type joinedMsg struct {
	name    string
	outcome outcome
	gone    bool // the session left the registry: it ended, rather than our link dropping
	err     error
}

// menu is the session list: type to filter, arrows to move, enter to join.
// It refreshes itself on a timer. Unlike fzf's reload, that never touches the
// filter being typed, and the cursor stays on the same session, not row.
type menu struct {
	cfg        menuConfig
	all        []session
	visible    []session
	cursor     int
	selectedID string
	query      textinput.Model
	notice     string
	width      int
	height     int
}

func newMenu(cfg menuConfig) *menu {
	if cfg.now == nil {
		cfg.now = time.Now
	}
	q := textinput.New()
	q.Prompt = "> "
	q.Placeholder = "type to filter"
	q.Focus()
	return &menu{cfg: cfg, query: q, width: 80, height: 24}
}

func runMenu(cfg menuConfig) int {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr, "ush: the menu needs a terminal: connect with ssh -t (or use: list, join <id>)")
		return 1
	}
	m := newMenu(cfg)
	m.reload()
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "ush: %v\n", err)
		return 1
	}
	return 0
}

func (m *menu) Init() tea.Cmd { return tick() }

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *menu) reload() {
	m.all = loadSessions(m.cfg.dir)
	for i := range m.all {
		m.all[i].Viewers = countViewers(m.cfg.viewers, m.all[i].ID)
	}
	m.refilter()
}

// refilter recomputes the visible list from the query, keeping the cursor on
// the selected session if it is still there.
func (m *menu) refilter() {
	q := m.query.Value()
	if q == "" {
		m.visible = slices.Clone(m.all)
	} else {
		keys := make([]string, len(m.all))
		for i, s := range m.all {
			keys[i] = s.Name + " " + s.Machine + " " + s.Command
		}
		m.visible = nil
		for _, match := range fuzzy.Find(q, keys) {
			m.visible = append(m.visible, m.all[match.Index])
		}
	}
	// A new filter lands on its best match, as fzf does, wherever the tree
	// puts it.
	if q != "" && m.selectedID == "" && len(m.visible) > 0 {
		m.selectedID = m.visible[0].ID
	}
	// Shown as a tree, each machine with its sessions under it.
	slices.SortStableFunc(m.visible, func(a, b session) int { return strings.Compare(a.Machine, b.Machine) })
	for i, s := range m.visible {
		if s.ID == m.selectedID {
			m.cursor = i
		}
	}
	m.cursor = max(0, min(m.cursor, len(m.visible)-1))
	if s := m.selected(); s != nil {
		m.selectedID = s.ID
	}
}

func (m *menu) selected() *session {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return nil
	}
	return &m.visible[m.cursor]
}

func (m *menu) move(delta int) {
	m.cursor = max(0, min(m.cursor+delta, len(m.visible)-1))
	if s := m.selected(); s != nil {
		m.selectedID = s.ID
	}
}

func (m *menu) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		m.reload()
		return m, tick()
	case joinedMsg:
		if msg.outcome == disconnected {
			return m, tea.Quit
		}
		m.notice = notice(msg)
		if msg.outcome == ended && msg.gone {
			m.query.SetValue("") // it would only match the session that just ended
		}
		m.reload()
		return m, tea.WindowSize() // the terminal may have been resized while joined
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyEsc:
			if m.query.Value() != "" {
				m.query.SetValue("")
				m.refilter()
				return m, nil
			}
			return m, tea.Quit
		case tea.KeyUp, tea.KeyCtrlP:
			m.move(-1)
			return m, nil
		case tea.KeyDown, tea.KeyCtrlN:
			m.move(1)
			return m, nil
		case tea.KeyCtrlR:
			m.reload()
			return m, nil
		case tea.KeyEnter:
			if s := m.selected(); s != nil && m.cfg.join != nil {
				return m, m.joinCmd(*s)
			}
			return m, nil
		}
		if len(m.all) == 0 && m.query.Value() == "" && msg.String() == "q" {
			return m, tea.Quit
		}
		before := m.query.Value()
		var cmd tea.Cmd
		m.query, cmd = m.query.Update(msg)
		if m.query.Value() != before {
			m.selectedID, m.cursor = "", 0 // new filter: refilter picks its best match
			m.refilter()
		}
		return m, cmd
	}
	return m, nil
}

func notice(j joinedMsg) string {
	switch {
	case j.outcome == detached:
		return fmt.Sprintf("left %q, still running (ctrl-] detaches)", j.name)
	case j.outcome == failed:
		return fmt.Sprintf("could not join %q: %v", j.name, j.err)
	case j.gone:
		return fmt.Sprintf("session %q ended", j.name)
	default:
		return fmt.Sprintf("lost connection to %q, still running", j.name)
	}
}

// --- view ------------------------------------------------------------------

var (
	selectedRow = lipgloss.NewStyle().Reverse(true)
	noticeStyle = lipgloss.NewStyle().Bold(true)
	cardPane    = lipgloss.NewStyle().BorderStyle(lipgloss.NormalBorder()).BorderLeft(true).PaddingLeft(1)
)

func (m *menu) View() string {
	var top []string
	if m.notice != "" {
		top = append(top, noticeStyle.Render(m.notice), "")
	}
	if len(m.all) == 0 {
		return strings.Join(append(top, "No active sessions; waiting for one to appear. q quits."), "\n")
	}

	help := faint.Render("enter joins · ctrl-] opens the session panel · esc quits")
	prompt := m.query.View() + "  " + faint.Render(fmt.Sprintf("%d/%d", len(m.visible), len(m.all)))

	// Fill the screen: the panes run the full height, with the filter pinned
	// to the last line.
	body := max(m.height-len(top)-2, 3) // help and prompt take the rest
	wide := m.width >= 90
	listWidth, cardWidth := m.width, m.width
	if wide {
		cardWidth = min(48, m.width/2)
		listWidth = m.width - cardWidth - 3
	}
	var detail string
	if s := m.selected(); s != nil {
		detail = card(*s, m.cfg.now(), cardWidth)
	}

	// Height pads but never cuts, so each pane is also capped at its height:
	// a card taller than its space would push the whole view past the
	// bottom of the window, and the top -- help, header, the list itself --
	// would scroll away.
	var pane string
	if wide {
		list := lipgloss.NewStyle().Width(listWidth).Height(body).MaxHeight(body).Render(m.renderList(listWidth, body))
		pane = lipgloss.JoinHorizontal(lipgloss.Top, list, " ", cardPane.Height(body).MaxHeight(body).Render(detail))
	} else {
		cardHeight := min(strings.Count(detail, "\n")+1, body/2)
		listHeight := body - cardHeight - 1
		list := m.renderList(listWidth, listHeight)
		pane = lipgloss.NewStyle().Height(listHeight).MaxHeight(listHeight).Render(list) + "\n" +
			faint.Render(strings.Repeat("─", max(listWidth, 1))) + "\n" +
			lipgloss.NewStyle().Height(cardHeight).MaxHeight(cardHeight).Render(detail)
	}
	return strings.Join(append(top, help, pane, prompt), "\n")
}

func (m *menu) renderList(width, rows int) string {
	now := m.cfg.now()
	nameW, ageW := 4, 3
	for _, s := range m.visible {
		nameW = max(nameW, min(ansi.StringWidth(s.Name)+2, 26)) // +2 for the branch
		ageW = max(ageW, len(age(now, s.StartedEpoch)))
	}
	const viewW = len("VIEWERS")
	cmdW := max(width-nameW-viewW-ageW-8, 8)

	line := func(marker, name, command, viewers, ago string) string {
		return marker + pad(name, nameW) + "  " + pad(command, cmdW) + "  " +
			fmt.Sprintf("%*s", viewW, viewers) + "  " + pad(ago, ageW)
	}
	out := []string{faint.Render(line("  ", "NAME", "COMMAND", "VIEWERS", "AGE"))}

	// The tree: a heading for each machine, its sessions on branches below.
	var tree []string
	cursorRow := 0
	for i, s := range m.visible {
		if i == 0 || s.Machine != m.visible[i-1].Machine {
			tree = append(tree, "  "+machineHeading(s.Machine))
		}
		branch := "├ "
		if i == len(m.visible)-1 || m.visible[i+1].Machine != s.Machine {
			branch = "└ "
		}
		ago := age(now, s.StartedEpoch)
		// Blank rather than 0, so the sessions someone is in stand out.
		viewers := ""
		if s.Viewers > 0 {
			viewers = strconv.Itoa(s.Viewers)
		}
		row := line("  ", branch+s.Name, s.Command, viewers, ago)
		if i == m.cursor {
			row = selectedRow.Render(line("▌ ", branch+s.Name, s.Command, viewers, ago))
			cursorRow = len(tree)
		}
		tree = append(tree, row)
	}

	// Scroll so the cursor stays on screen.
	start := 0
	if len(tree) > rows-1 {
		start = max(0, min(cursorRow-(rows-1)/2, len(tree)-(rows-1)))
	}
	out = append(out, tree[start:min(len(tree), start+rows-1)]...)
	return strings.Join(out, "\n")
}

func machineHeading(machine string) string {
	if machine == "" {
		return faint.Render("unknown machine")
	}
	return bold.Foreground(machineColor(machine)).Render(machine)
}

// machinePalette is the iOS app's, in its order: SwiftUI's blue, green,
// purple, teal, pink, indigo, mint and brown, as they look in dark mode.
// Fixed rather than adaptive: picking by background would query the
// terminal, which RestoreTerm exists to avoid.
var machinePalette = []lipgloss.Color{"#0A84FF", "#30D158", "#BF5AF2", "#40C8E0", "#FF375F", "#5E5CE6", "#66D4CF", "#AC8E68"}

// machineColor gives a machine the same colour the iOS app does: FNV-1a over
// the name picks from the same palette.
func machineColor(machine string) lipgloss.Color {
	h := fnv.New32a()
	h.Write([]byte(machine))
	return machinePalette[h.Sum32()%uint32(len(machinePalette))]
}

// pad truncates or pads s to exactly w cells.
func pad(s string, w int) string {
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", max(w-ansi.StringWidth(s), 0))
}

// --- joining ---------------------------------------------------------------

func (m *menu) joinCmd(s session) tea.Cmd {
	j := &joinExec{t: m.cfg.join(s), dir: m.cfg.dir, s: s}
	return tea.Exec(j, func(error) tea.Msg {
		return joinedMsg{name: currentName(m.cfg.dir, s), outcome: j.outcome, gone: j.gone, err: j.err}
	})
}

// joinExec runs a join while bubbletea has handed over the terminal.
type joinExec struct {
	t       target
	dir     string
	s       session
	outcome outcome
	gone    bool
	err     error
}

func (j *joinExec) Run() error {
	j.outcome, j.err = joinSession(j.t, os.Stdin, os.Stdout)
	if j.outcome == ended {
		// The sharer deregisters as soon as its session ends, so a vanished
		// entry means the session is over rather than our link dropping.
		for i := 0; i < 10 && !j.gone; i++ {
			j.gone = sessionGone(j.dir, j.s.ID)
			if !j.gone {
				time.Sleep(200 * time.Millisecond)
			}
		}
	}
	return nil
}

func (j *joinExec) SetStdin(io.Reader)  {}
func (j *joinExec) SetStdout(io.Writer) {}
func (j *joinExec) SetStderr(io.Writer) {}
