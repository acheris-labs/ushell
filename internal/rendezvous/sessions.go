package rendezvous

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// session is one registry entry, as ush-rendezvous writes it.
type session struct {
	ID           string `json:"session_id"`
	Name         string `json:"name"`
	Machine      string `json:"machine"`
	User         string `json:"user"`
	Command      string `json:"command"`
	Cwd          string `json:"cwd"`
	Description  string `json:"description"`
	ReadOnly     bool   `json:"read_only"`
	Started      string `json:"started"`
	StartedEpoch int64  `json:"started_epoch"`
	// Viewers is how many are joined right now. It is counted when the list
	// is read, never stored, and left out of the JSON while it is zero.
	Viewers int `json:"viewers,omitempty"`
}

// loadSessions reads the registry, sorted by name then age. Unreadable or
// half-written entries are skipped; the next refresh picks them up.
func loadSessions(dir string) []session {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var out []session
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var s session
		if json.Unmarshal(b, &s) != nil || s.ID == "" {
			continue
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].StartedEpoch < out[j].StartedEpoch
	})
	return out
}

func sessionGone(dir, id string) bool {
	_, err := os.Stat(filepath.Join(dir, id+".json"))
	return errors.Is(err, fs.ErrNotExist)
}

// age is how long ago epoch was, as 42s / 7m / 3h / 2d.
func age(now time.Time, epoch int64) string {
	s := now.Unix() - epoch
	switch {
	case s < 60:
		return itoa(max(s, 0)) + "s"
	case s < 3600:
		return itoa(s/60) + "m"
	case s < 86400:
		return itoa(s/3600) + "h"
	default:
		return itoa(s/86400) + "d"
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// renameSession changes a session's name in the registry, which is what every
// menu shows. The sharer's own USH_SESSION keeps the old name.
func renameSession(dir, id, name string) error {
	path := filepath.Join(dir, id+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var entry map[string]any // keep every field, not just the ones we know
	if err := json.Unmarshal(b, &entry); err != nil {
		return err
	}
	entry["name"] = name
	out, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".rename-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// readSession reads one registry entry.
func readSession(dir, id string) (session, bool) {
	if dir == "" {
		return session{}, false
	}
	b, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		return session{}, false
	}
	var s session
	if json.Unmarshal(b, &s) != nil {
		return session{}, false
	}
	return s, true
}

// currentName is s's name as the registry has it now: someone may have
// renamed it while we were joined.
func currentName(dir string, s session) string {
	if now, ok := readSession(dir, s.ID); ok && now.Name != "" {
		return now.Name
	}
	return s.Name
}
