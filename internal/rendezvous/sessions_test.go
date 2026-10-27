package rendezvous

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeSession(t *testing.T, dir string, s session) {
	t.Helper()
	b, _ := json.Marshal(s)
	if err := os.WriteFile(filepath.Join(dir, s.ID+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSessionsSortsAndSkipsJunk(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, session{ID: "BBBBBBBBBBBBBBBBBBBB", Name: "zeta", StartedEpoch: 1})
	writeSession(t, dir, session{ID: "AAAAAAAAAAAAAAAAAAAA", Name: "alpha", StartedEpoch: 2})
	os.WriteFile(filepath.Join(dir, "CCCCCCCCCCCCCCCCCCCC.json"), []byte("{not json"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0o644)

	got := loadSessions(dir)
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Fatalf("want [alpha zeta], got %+v", got)
	}
}

func TestAge(t *testing.T) {
	now := time.Unix(100000, 0)
	for secs, want := range map[int64]string{5: "5s", 125: "2m", 7300: "2h", 200000: "2d"} {
		if got := age(now, now.Unix()-secs); got != want {
			t.Errorf("%ds: got %s want %s", secs, got, want)
		}
	}
}

func TestCurrentNameFollowsRenames(t *testing.T) {
	dir := t.TempDir()
	s := session{ID: "AAAAAAAAAAAAAAAAAAAA", Name: "oldname"}
	writeSession(t, dir, s)
	renameSession(dir, s.ID, "newname")
	if got := currentName(dir, s); got != "newname" {
		t.Fatalf("got %q, want the renamed name", got)
	}
	os.Remove(filepath.Join(dir, s.ID+".json"))
	if got := currentName(dir, s); got != "oldname" {
		t.Fatalf("an ended session should keep the name it had, got %q", got)
	}
}
