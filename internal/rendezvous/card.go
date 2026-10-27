package rendezvous

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var (
	faint = lipgloss.NewStyle().Faint(true)
	bold  = lipgloss.NewStyle().Bold(true)
)

// card describes one session for the detail pane: short labelled rows rather
// than the raw registry JSON.
func card(s session, now time.Time, width int) string {
	access := "can type"
	if s.ReadOnly {
		access = "watch only"
	}
	rows := [][2]string{
		{"machine", s.Machine},
		{"user", s.User},
		{"command", s.Command},
		{"cwd", s.Cwd},
	}
	if s.Description != "" {
		rows = append(rows, [2]string{"about", s.Description})
	}
	viewers := "nobody"
	if s.Viewers > 0 {
		viewers = fmt.Sprintf("%d joined", s.Viewers)
	}
	rows = append(rows,
		[2]string{"access", access},
		[2]string{"viewers", viewers},
		[2]string{"started", startedText(s, now)},
	)

	valueWidth := max(width-9, 10)
	wrap := lipgloss.NewStyle().Width(valueWidth)
	lines := []string{bold.Render(s.Name), ""}
	for _, r := range rows {
		value := strings.Split(wrap.Render(r[1]), "\n")
		lines = append(lines, faint.Render(fmt.Sprintf("%-7s", r[0]))+"  "+value[0])
		for _, more := range value[1:] {
			lines = append(lines, strings.Repeat(" ", 9)+more)
		}
	}
	lines = append(lines, "", faint.Render("session "+s.ID))
	return strings.Join(lines, "\n")
}

func startedText(s session, now time.Time) string {
	ago := age(now, s.StartedEpoch) + " ago"
	t, err := time.Parse(time.RFC3339, s.Started)
	if err != nil {
		return ago
	}
	return t.UTC().Format("2006-01-02 15:04 UTC") + " (" + ago + ")"
}
