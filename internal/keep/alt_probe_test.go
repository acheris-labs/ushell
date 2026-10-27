package keep

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A full-screen program asks its terminal questions -- device attributes,
// cursor position -- and waits for the answers. The emulator answers through a
// pipe, and a pipe blocks until it is read, so an undrained one wedges the
// emulator on the first query and every attached terminal with it. A shell
// never asks, which is why this needs a program that does.
func TestAFullScreenProgramDoesNotWedgeTheSession(t *testing.T) {
	if _, err := os.Stat("/usr/bin/vi"); err != nil {
		t.Skip("vi not installed")
	}
	withHome(t)
	file := t.TempDir() + "/file.txt"
	start(t, "alt", "/usr/bin/vi", file)
	time.Sleep(2 * time.Second) // let it draw

	c := attach(t, "alt", false)
	got := c.waitFor("~", 8*time.Second)
	if !strings.Contains(got, "~") {
		t.Fatalf("attaching to a full-screen program showed nothing: %q", got)
	}

	// And the session is still live afterwards, not stuck mid-query.
	c.typ(":q!\r")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !Exists("alt") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the session did not respond to input after attaching")
}
