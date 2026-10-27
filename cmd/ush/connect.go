package main

import (
	"cmp"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"ush/internal/config"
)

// What a direct join exits with when the user picks from the Ctrl-] panel:
// rendezvous.ExitDetached and ExitQuit, copied so ush need not link the menu.
const (
	joinBackToList = 10
	joinDisconnect = 11
)

// connect opens the menu, or with a target joins that session directly. The
// rendezvous joins only by id, so a name is looked up in its list first;
// machine, if set, narrows that list to what was shared from there.
func connect(cfg config.Config, target, machine string) (int, error) {
	if target == "" {
		return browse(cfg)
	}
	sessions, err := liveSessions(cfg)
	if err != nil {
		return 0, err
	}
	id, err := findSession(sessions, target, machine)
	if err != nil {
		return 0, err
	}
	// The Ctrl-] panel's choices: "back to list" opens the menu the session
	// was never picked from, and "disconnect" is an ordinary exit.
	code, err := browse(cfg, "join", id)
	switch {
	case err != nil:
		return code, err
	case code == joinBackToList:
		return browse(cfg)
	case code == joinDisconnect:
		return 0, nil
	}
	return code, nil
}

// liveSessions asks the rendezvous what is live.
func liveSessions(cfg config.Config) ([]listedSession, error) {
	signer, err := loadSigner(cfg.Key)
	if err != nil {
		return nil, err
	}
	addr := net.JoinHostPort(cfg.Rendezvous, cfg.Port)
	sessions, _, err := listRendezvous(cfg, signer, addr)
	if err != nil {
		return nil, fmt.Errorf("rendezvous %s: %s", addr, explain(err))
	}
	return sessions, nil
}

// onMachine keeps the sessions shared from machine, or all of them when it is
// empty. Machine names are hostnames, so case does not matter. It also says
// so, for messages: ` on "nas"`.
func onMachine(sessions []listedSession, machine string) ([]listedSession, string) {
	if machine == "" {
		return sessions, ""
	}
	return slices.DeleteFunc(slices.Clone(sessions), func(s listedSession) bool {
		return !strings.EqualFold(s.Machine, machine)
	}), fmt.Sprintf(" on %q", machine)
}

// listLive is ush connect --list.
func listLive(cfg config.Config, machine string, out io.Writer) error {
	sessions, err := liveSessions(cfg)
	if err != nil {
		return err
	}
	printSessions(out, sessions, machine, time.Now())
	return nil
}

// printSessions writes one row per session, by name then machine, with the
// full id for ush connect ID.
func printSessions(out io.Writer, sessions []listedSession, machine string, now time.Time) {
	sessions, on := onMachine(sessions, machine)
	if len(sessions) == 0 {
		fmt.Fprintf(out, "no sessions are live%s\n", on)
		return
	}
	sessions = slices.Clone(sessions)
	slices.SortFunc(sessions, func(a, b listedSession) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Machine, b.Machine))
	})
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tMACHINE\tUSER\tUP\tID")
	for _, s := range sessions {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.Name, s.Machine, s.User, up(now.Sub(time.Unix(s.StartedEpoch, 0))), s.ID)
	}
	_ = w.Flush()
}

// up rounds how long a session has been up to its largest unit.
func up(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// findSession picks the one session target means: its id, or else its name,
// among those shared from machine when that is set. Names are not unique, so
// two of a name are refused with their ids.
func findSession(sessions []listedSession, target, machine string) (string, error) {
	sessions, on := onMachine(sessions, machine)
	var named []listedSession
	for _, s := range sessions {
		if s.ID == target {
			return s.ID, nil
		}
		if s.Name == target {
			named = append(named, s)
		}
	}
	switch len(named) {
	case 1:
		return named[0].ID, nil
	case 0:
		if len(sessions) == 0 {
			return "", fmt.Errorf("no session named %q: no sessions are live%s", target, on)
		}
		var names []string
		for _, s := range sessions {
			names = append(names, s.Name)
		}
		slices.Sort(names)
		return "", fmt.Errorf("no session named %q%s; live: %s", target, on, strings.Join(slices.Compact(names), ", "))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d sessions are named %q%s; connect to one by id", len(named), target, on)
	if slices.ContainsFunc(named, func(s listedSession) bool { return !strings.EqualFold(s.Machine, named[0].Machine) }) {
		b.WriteString(", or pick its machine with -m")
	}
	b.WriteString(":")
	for _, s := range named {
		fmt.Fprintf(&b, "\n  ush connect %s   # %s@%s", s.ID, s.User, s.Machine)
	}
	return "", fmt.Errorf("%s", b.String())
}
