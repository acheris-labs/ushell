package rendezvous

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// panel is what Ctrl-] opens inside a joined session: where you are, who else
// is watching, and what to do next. The session keeps running meanwhile.
type panel struct {
	info     panelInfo
	rename   func(string) error // nil: renaming unavailable
	now      func() time.Time
	cursor   int
	renaming bool
	input    textinput.Model
	message  string
	choice   panelChoice
	width    int
	height   int
}

type panelInfo struct {
	session session
	viewers int
	joined  time.Time
}

type panelChoice int

const (
	choiceBack panelChoice = iota
	choiceList
	choiceDisconnect
	choiceRedraw // repaint the session: a viewer sees only what is drawn after joining
)

var validSessionName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

type panelItem struct {
	key, label string
}

var panelItems = []panelItem{
	{"b", "back to session"},
	{"r", "refresh the screen"},
	{"l", "back to list"},
	{"n", "rename…"},
	{"d/q", "disconnect"},
}

func newPanel(info panelInfo, rename func(string) error, now func() time.Time) *panel {
	in := textinput.New()
	in.Prompt = "new name: "
	in.CharLimit = 32
	return &panel{info: info, rename: rename, now: now, input: in}
}

func (p *panel) Init() tea.Cmd { return nil }

func (p *panel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		p.width, p.height = size.Width, size.Height
		return p, nil
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return p, nil
	}
	if p.renaming {
		return p.updateRename(key)
	}
	switch key.String() {
	case "esc", "b", "ctrl+c":
		return p.choose(choiceBack)
	case "l":
		return p.choose(choiceList)
	case "d", "q":
		return p.choose(choiceDisconnect)
	case "r":
		return p.choose(choiceRedraw)
	case "n":
		return p.startRename()
	case "up", "ctrl+p", "k":
		p.cursor = max(p.cursor-1, 0)
	case "down", "ctrl+n", "j":
		p.cursor = min(p.cursor+1, len(panelItems)-1)
	case "enter":
		switch panelItems[p.cursor].key {
		case "b":
			return p.choose(choiceBack)
		case "r":
			return p.choose(choiceRedraw)
		case "l":
			return p.choose(choiceList)
		case "n":
			return p.startRename()
		case "d/q":
			return p.choose(choiceDisconnect)
		}
	}
	return p, nil
}

func (p *panel) choose(c panelChoice) (tea.Model, tea.Cmd) {
	p.choice = c
	return p, tea.Quit
}

func (p *panel) startRename() (tea.Model, tea.Cmd) {
	if p.rename == nil {
		p.message = "renaming isn't available here"
		return p, nil
	}
	p.renaming, p.message = true, ""
	p.input.SetValue("")
	p.input.Placeholder = p.info.session.Name
	return p, p.input.Focus()
}

func (p *panel) updateRename(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyEsc:
		p.renaming, p.message = false, ""
		return p, nil
	case tea.KeyEnter:
		name := strings.TrimSpace(p.input.Value())
		if !validSessionName.MatchString(name) {
			p.message = "use 1-32 letters, digits, . _ -"
			return p, nil
		}
		if err := p.rename(name); err != nil {
			p.message = "rename failed: " + err.Error()
			return p, nil
		}
		p.info.session.Name = name
		p.renaming, p.message = false, "renamed; the list shows it for everyone"
		return p, nil
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(key)
	return p, cmd
}

var panelBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)

func (p *panel) View() string {
	s := p.info.session
	now := p.now()
	lines := []string{
		bold.Render(s.Name) + faint.Render(" · "+s.Machine+" · "+s.Command),
		"",
		faint.Render("viewers now     ") + fmt.Sprintf("%d (incl. you)", p.info.viewers),
		faint.Render("you've been on  ") + duration(now.Sub(p.info.joined)),
		faint.Render("session up      ") + duration(now.Sub(time.Unix(s.StartedEpoch, 0))),
		"",
	}
	if p.renaming {
		lines = append(lines, p.input.View(), faint.Render("enter saves · esc cancels"))
	} else {
		for i, item := range panelItems {
			marker := "  "
			label := item.label
			if i == p.cursor {
				marker, label = "▸ ", bold.Render(label)
			}
			lines = append(lines, marker+faint.Render(fmt.Sprintf("%-3s", item.key))+"  "+label)
		}
		lines = append(lines, "", faint.Render("esc goes back to the session"))
	}
	if p.message != "" {
		lines = append(lines, "", p.message)
	}
	box := panelBox.Render(strings.Join(lines, "\n"))
	if p.width == 0 || p.height == 0 {
		return box // size not known yet
	}
	return lipgloss.Place(p.width, p.height, lipgloss.Center, lipgloss.Center, box)
}

// duration reads like 42s, 12m, 3h 04m.
func duration(d time.Duration) string {
	d = max(d, 0).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
