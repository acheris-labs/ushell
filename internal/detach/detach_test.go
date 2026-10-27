package detach

import (
	"bytes"
	"testing"
)

func TestScanDetach(t *testing.T) {
	cases := []struct {
		name     string
		in       []byte
		forward  []byte
		detached bool
	}{
		{"plain input passes through", []byte("ls -la\r"), []byte("ls -la\r"), false},
		{"chord alone", []byte{Key}, []byte{}, true},
		{"mid-line, no Enter needed", []byte("abc\x1d"), []byte("abc"), true},
		{"bytes after the chord are dropped", []byte("ab\x1dcd"), []byte("ab"), true},
		{"other control keys pass through", []byte("\x03\x04\x1c"), []byte("\x03\x04\x1c"), false},

		// A program that turns on the Kitty keyboard protocol -- herdr does --
		// makes the terminal report Ctrl-] as an escape sequence instead.
		{"kitty encoding", []byte("\x1b[93;5u"), []byte{}, true},
		{"kitty encoding with an explicit press event", []byte("\x1b[93;5:1u"), []byte{}, true},
		{"kitty encoding after typing", []byte("abc\x1b[93;5u"), []byte("abc"), true},
		{"ctrl+shift+] still detaches", []byte("\x1b[93;6u"), []byte{}, true},
		{"a release is not a press", []byte("\x1b[93;5:3u"), []byte("\x1b[93;5:3u"), false},
		{"a repeat is not a fresh press", []byte("\x1b[93;5:2u"), []byte("\x1b[93;5:2u"), false},
		{"] without ctrl passes through", []byte("\x1b[93;1u"), []byte("\x1b[93;1u"), false},
		{"another ctrl key passes through", []byte("\x1b[97;5u"), []byte("\x1b[97;5u"), false},
		{"arrow keys pass through", []byte("\x1b[A\x1b[B"), []byte("\x1b[A\x1b[B"), false},
		{"bare escape passes through at once", []byte("\x1b"), []byte("\x1b"), false},
		{"escape then text", []byte("\x1bOP"), []byte("\x1bOP"), false},
		{"mouse report passes through", []byte("\x1b[<0;10;5M"), []byte("\x1b[<0;10;5M"), false},
		{"bracketed paste passes through", []byte("\x1b[200~hi\x1b[201~"), []byte("\x1b[200~hi\x1b[201~"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var s Scanner
			forward, detached := s.Scan(c.in)
			if !bytes.Equal(forward, c.forward) || detached != c.detached {
				t.Fatalf("Scan(%q) = %q, %v; want %q, %v", c.in, forward, detached, c.forward, c.detached)
			}
		})
	}
}

// An escape sequence does not have to arrive in one read.
func TestScanDetachAcrossReads(t *testing.T) {
	cases := []struct {
		name  string
		reads []string
	}{
		{"split in the parameters", []string{"\x1b[93", ";5u"}},
		{"split after the bracket", []string{"\x1b[", "93;5u"}},
		{"typing first", []string{"ls\x1b[93;", "5u"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var s Scanner
			var forwarded []byte
			for i, r := range c.reads {
				out, detached := s.Scan([]byte(r))
				forwarded = append(forwarded, out...)
				if detached {
					if i != len(c.reads)-1 {
						t.Fatalf("detached on read %d of %d", i+1, len(c.reads))
					}
					if bytes.Contains(forwarded, []byte("93")) {
						t.Fatalf("leaked part of the sequence to the session: %q", forwarded)
					}
					return
				}
			}
			t.Fatalf("never detached; forwarded %q", forwarded)
		})
	}
}

// A read that ends on the bare ESC is the one split we do not recognise: the
// ESC goes to the program at once so Escape does not feel stuck in vi, and by
// the time the rest arrives there is nothing to attach it to. Terminals send a
// keystroke's bytes in one write, so this needs a read boundary inside one
// keypress.
func TestScanDetachForwardsABareEscapeAtOnce(t *testing.T) {
	var s Scanner
	out, detached := s.Scan([]byte("\x1b"))
	if detached || string(out) != "\x1b" {
		t.Fatalf("bare escape should pass through immediately: %q, %v", out, detached)
	}
	if out, detached := s.Scan([]byte("[93;5u")); detached {
		t.Fatalf("nothing to attach the rest to, so it is forwarded: %q", out)
	}
}

// A partial sequence that turns out to be something else must reach the
// program rather than being swallowed.
func TestScanDetachReleasesWhatItHeld(t *testing.T) {
	var s Scanner
	if out, detached := s.Scan([]byte("\x1b[93;")); len(out) != 0 || detached {
		t.Fatalf("held partial should forward nothing yet: %q, %v", out, detached)
	}
	out, detached := s.Scan([]byte("5~"))
	if detached {
		t.Fatal("CSI 93;5~ is not the detach key")
	}
	if string(out) != "\x1b[93;5~" {
		t.Fatalf("held bytes were lost: %q", out)
	}
}
