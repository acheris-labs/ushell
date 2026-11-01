package keep

import (
	"os"
	"strings"
	"testing"
	"time"

	"ush/internal/register"
)

// keys types each string as its own keystroke burst, as a person would.
func keys(t *testing.T, bursts ...string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	go func() {
		defer w.Close()
		for _, b := range bursts {
			time.Sleep(30 * time.Millisecond)
			_, _ = w.WriteString(b)
		}
	}()
	return r
}

func sharedLabel(t *testing.T) (*register.Label, string) {
	t.Helper()
	withHome(t)
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	label := register.NewLabel("api", "")
	ctl := register.ControlPath(dir, "api")
	ln, err := register.ServeControl(ctl, label)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return label, ctl
}

func TestTheDetachScreenRelabelsASharedSession(t *testing.T) {
	label, ctl := sharedLabel(t)
	var out strings.Builder
	// e, a description with a typo fixed, Enter; n, a new name, Enter; then
	// any other key goes back to the session.
	detached := confirmDetach("api", ctl, keys(t, "e", "pr monitpr", "\x7f\x7for\r", "n", "\x15api-v2\r", "x"), &out)
	if detached {
		t.Fatal("x should go back to the session, not detach")
	}
	if n, d := label.Get(); n != "api-v2" || d != "pr monitor" {
		t.Fatalf("label is %q, %q", n, d)
	}
	for _, want := range []string{"n  rename", "e  description", "saved; the menu gets it", "renamed;", "listed as     api-v2"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the screen never showed %q", want)
		}
	}
}

func TestTheDetachScreenSaysWhyAnEditFailed(t *testing.T) {
	label, ctl := sharedLabel(t)
	var out strings.Builder
	if confirmDetach("api", ctl, keys(t, "n", "\x15bad name\r", "e", "dropped\x1b", "d"), &out) != true {
		t.Fatal("d should detach")
	}
	if n, d := label.Get(); n != "api" || d != "" {
		t.Fatalf("label changed to %q, %q", n, d)
	}
	if !strings.Contains(out.String(), "not saved: a name is 1-32") {
		t.Errorf("the refusal was not shown:\n%q", out.String())
	}
}

func TestTheDetachScreenOfAnUnsharedSessionOnlyDetaches(t *testing.T) {
	withHome(t)
	var out strings.Builder
	if confirmDetach("api", "", keys(t, "n"), &out) {
		t.Fatal("n on an unshared session should go back, as any other key does")
	}
	if strings.Contains(out.String(), "rename") {
		t.Error("an unshared session should not offer to rename")
	}
}
