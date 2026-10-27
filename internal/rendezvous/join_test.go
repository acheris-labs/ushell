package rendezvous

import "testing"

func TestTitleNamesTheSession(t *testing.T) {
	dir := t.TempDir()
	s := session{ID: "AAAAAAAAAAAAAAAAAAAA", Name: "purple", Machine: "lumen"}
	if err := writeEntry(dir, s); err != nil {
		t.Fatal(err)
	}
	if got, want := sessionTitle(dir, s.ID), "\x1b]2;purple@lumen\x07"; got != want {
		t.Errorf("sessionTitle = %q, want %q", got, want)
	}
}

func TestTitleLeftAloneWithoutTheSession(t *testing.T) {
	if got := sessionTitle(t.TempDir(), "AAAAAAAAAAAAAAAAAAAA"); got != "" {
		t.Errorf("sessionTitle = %q, want nothing", got)
	}
	if got := sessionTitle("", "AAAAAAAAAAAAAAAAAAAA"); got != "" {
		t.Errorf("sessionTitle with no registry = %q, want nothing", got)
	}
}
