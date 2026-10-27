package rendezvous

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// payload builds what a sharer sends: base64 of key=value lines.
func payload(lines ...string) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n")))
}

var when = time.Date(2026, 9, 21, 10, 30, 0, 0, time.UTC)

func TestRegistrationValidation(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  session // zero fields are not checked
		bad   string  // expected error, if any
	}{
		{
			name:  "a whole registration",
			lines: []string{"name=api", "machine=umber", "user=alex", "command=/bin/sh", "cwd=/tmp", "description=the api", "read_only=true"},
			want:  session{Name: "api", Machine: "umber", User: "alex", Command: "/bin/sh", Cwd: "/tmp", Description: "the api", ReadOnly: true},
		},
		{name: "a name with a space is refused", lines: []string{"name=my api"}, bad: "bad name"},
		{name: "an empty name is refused", lines: []string{"name="}, bad: "bad name"},
		{
			name:  "a missing machine becomes unknown",
			lines: []string{"name=api"},
			want:  session{Machine: "unknown"},
		},
		{
			name:  "a machine of only non-printables becomes unknown",
			lines: []string{"name=api", "machine=\x01\x02"},
			want:  session{Machine: "unknown"},
		},
		{
			name:  "a user that is not a username is dropped, not refused",
			lines: []string{"name=api", "user=not a user"},
			want:  session{User: ""},
		},
		{
			name:  "read_only is that exact word and nothing else",
			lines: []string{"name=api", "read_only=TRUE"},
			want:  session{ReadOnly: false},
		},
		{
			name:  "unknown keys are dropped",
			lines: []string{"name=api", "evil=../../etc/passwd"},
			want:  session{Name: "api"},
		},
		{
			name:  "a repeated key keeps its last value",
			lines: []string{"name=first", "name=second"},
			want:  session{Name: "second"},
		},
		{
			name:  "lines without a separator are ignored",
			lines: []string{"name=api", "garbage"},
			want:  session{Name: "api"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseRegistration(payload(c.lines...), when)
			if c.bad != "" {
				if err == nil || !strings.Contains(err.Error(), c.bad) {
					t.Fatalf("error = %v, want one containing %q", err, c.bad)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, f := range []struct{ name, got, want string }{
				{"name", got.Name, c.want.Name},
				{"machine", got.Machine, c.want.Machine},
				{"user", got.User, c.want.User},
				{"command", got.Command, c.want.Command},
				{"cwd", got.Cwd, c.want.Cwd},
				{"description", got.Description, c.want.Description},
			} {
				if c.want.Name != "" || f.name != "name" {
					if f.want != "" && f.got != f.want {
						t.Errorf("%s = %q, want %q", f.name, f.got, f.want)
					}
				}
			}
			if got.ReadOnly != c.want.ReadOnly {
				t.Errorf("read_only = %v, want %v", got.ReadOnly, c.want.ReadOnly)
			}
			if got.Started != "2026-09-21T10:30:00Z" || got.StartedEpoch != when.Unix() {
				t.Errorf("start time = %q / %d", got.Started, got.StartedEpoch)
			}
		})
	}
}

// The shell did this in the C locale, where isprint is ASCII 0x20-0x7e and the
// caps count bytes. Widening it to unicode.IsPrint would let combining marks,
// double-width characters and homoglyphs onto another viewer's screen.
func TestCleanKeepsPrintableASCIIAndCountsBytes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain text", "plain text"},
		{"drop\x00nulls\x1b[31m", "dropnulls[31m"},
		{"naïve", "nave"},
		{"emoji 🙂 gone", "emoji  gone"},
		{"tabs\tand\nnewlines", "tabsandnewlines"},
	}
	for _, c := range cases {
		if got := clean(c.in, 80); got != c.want {
			t.Errorf("clean(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := clean(strings.Repeat("x", 100), 32); len(got) != 32 {
		t.Errorf("cap = %d bytes, want 32", len(got))
	}
}

func TestRegistrationRejectsNonBase64(t *testing.T) {
	if _, err := parseRegistration("not base64!", when); err == nil {
		t.Fatal("a payload that is not base64 must be refused")
	}
}

// Clients grep this output, so its spelling is part of the contract: the same
// two-space indent, spacing and field order that `jq -s` produced.
func TestListIsSpelledLikeJQ(t *testing.T) {
	dir := t.TempDir()
	if got := string(listJSON(dir, "")); got != "[]\n" {
		t.Fatalf("empty registry = %q, want %q", got, "[]\n")
	}

	s, err := parseRegistration(payload("name=api", "machine=umber"), when)
	if err != nil {
		t.Fatal(err)
	}
	s.ID = "AbCdEfGhIjKlMnOpQrSt"
	if err := writeEntry(dir, s); err != nil {
		t.Fatal(err)
	}
	got := string(listJSON(dir, ""))
	for _, want := range []string{
		"[\n  {\n    \"session_id\": \"AbCdEfGhIjKlMnOpQrSt\",",
		"\n    \"name\": \"api\",",
		"\n    \"machine\": \"umber\",",
		fmt.Sprintf("\n    \"started_epoch\": %d\n  }\n]\n", when.Unix()),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("list output missing %q:\n%s", want, got)
		}
	}
}

func TestEntryLivesAndDies(t *testing.T) {
	dir := t.TempDir()
	s, err := parseRegistration(payload("name=api"), when)
	if err != nil {
		t.Fatal(err)
	}
	s.ID = "AbCdEfGhIjKlMnOpQrSt"
	if err := writeEntry(dir, s); err != nil {
		t.Fatal(err)
	}
	if sessionGone(dir, s.ID) {
		t.Fatal("entry should exist after writing it")
	}
	// Nothing half-written is ever visible: the file appears whole or not at all.
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("registry holds %d files, want exactly the entry", len(entries))
	}
	removeEntry(dir, s.ID)
	if !sessionGone(dir, s.ID) {
		t.Fatal("entry should be gone after removing it")
	}
}

func TestSessionIDsAreTwentyAlphanumerics(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id, err := newSessionID()
		if err != nil {
			t.Fatal(err)
		}
		if !validSessionID.MatchString(id) {
			t.Fatalf("id %q does not match %s", id, validSessionID)
		}
		if seen[id] {
			t.Fatalf("id %q repeated", id)
		}
		seen[id] = true
	}
}

func TestASessionFieldFromTheSharerIsIgnored(t *testing.T) {
	s, err := parseRegistration(payload("session=AbCdEfGhIjKlMnOpQrSt", "name=api"), when)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "" {
		t.Fatalf("a sharer-chosen id was kept: %q", s.ID)
	}
}

func TestListJSONCountsViewers(t *testing.T) {
	dir, viewers := t.TempDir(), t.TempDir()
	s, err := parseRegistration(payload("name=api"), when)
	if err != nil {
		t.Fatal(err)
	}
	s.ID = "AbCdEfGhIjKlMnOpQrSt"
	if err := writeEntry(dir, s); err != nil {
		t.Fatal(err)
	}
	leave, err := markViewer(viewers, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("\n    \"started_epoch\": %d,\n    \"viewers\": 1\n  }\n]\n", when.Unix())
	if got := string(listJSON(dir, viewers)); !strings.HasSuffix(got, want) {
		t.Errorf("list output should end with %q:\n%s", want, got)
	}
	// Nobody watching: the field is left out, as before it existed.
	leave()
	if got := string(listJSON(dir, viewers)); strings.Contains(got, "viewers") {
		t.Errorf("an unwatched session should have no viewers field:\n%s", got)
	}
}
