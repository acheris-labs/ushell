package detach

import (
	"strconv"
	"strings"
)

// Package detach recognises the detach key.
//
// A terminal can send Ctrl-] two ways, and which one it picks is not the
// user's choice: it depends on what the shared program asked for.
//
// Normally it is the byte 0x1d. But a program that turns on the Kitty keyboard
// protocol -- herdr pushes flags 7 on startup, and tmux can be configured to --
// makes a terminal that implements the protocol send `ESC [ 93 ; 5 u` instead:
// 93 is ']', and the modifier field says ctrl. Ghostty, kitty and WezTerm
// implement it; ShellFish on the iPad does not, which is why the panel kept
// opening there while it stopped working on the Mac.
//
// This only started mattering when each viewer gained its own multiplexer
// client, because before that the program enabled the protocol on the sharer's
// terminal, not on the viewer's.
// Key is Ctrl-], the telnet convention.
const Key = 0x1d

// The protocol keeps a stack of modes. Pushing plain encoding while the panel
// is up, and popping afterwards, hands the panel ordinary keystrokes and gives
// the program back whatever it had set, without having to know what that was.
const (
	PushPlainKeys = "\x1b[>0u"
	PopKeys       = "\x1b[<u"
)

// ResetModes turns off what a program may have left on in a terminal it no
// longer owns: mouse reporting in every encoding, focus reports, bracketed
// paste, a pushed keyboard mode, a hidden cursor, colours. Restoring raw mode
// undoes none of those, and left on they type mouse coordinates into the
// shell. Every way out of a session writes it, best effort: a killed process
// can't.
const ResetModes = "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1005l\x1b[?1006l\x1b[?1015l" +
	"\x1b[?1004l\x1b[?2004l" + PopKeys + "\x1b[?25h\x1b[0m"

// csiLimit caps how far we will look for a sequence's final byte before
// deciding it is not one we understand and passing it through untouched.
const csiLimit = 32

// detachScanner splits a keystroke stream into what to forward and whether the
// detach key was pressed. It holds state because an escape sequence can arrive
// split across reads.
//
// A bare ESC is always forwarded at once rather than held: waiting for the
// next byte would make Escape feel stuck in vi. The cost is that a sequence
// split exactly between the ESC and the '[' is not recognised, which needs a
// read boundary inside one keystroke.
// Scanner splits a keystroke stream into what to forward and whether the
// detach key was pressed.
type Scanner struct {
	pending []byte
}

// scan returns the input to forward and whether the detach key was pressed.
// Anything typed after the key in the same read is dropped.
func (s *Scanner) Scan(in []byte) ([]byte, bool) {
	buf := in
	if len(s.pending) > 0 {
		buf = append(s.pending, in...)
		s.pending = nil
	}

	out := make([]byte, 0, len(buf))
	for i := 0; i < len(buf); i++ {
		b := buf[i]
		if b == Key {
			return out, true
		}
		if b != 0x1b || i+1 >= len(buf) || buf[i+1] != '[' {
			out = append(out, b)
			continue
		}

		seq, complete := csiSequence(buf[i:])
		if !complete {
			// Hold a partial sequence only while it could still become the
			// detach key; anything else goes on to the program now.
			if couldBeDetach(buf[i:]) {
				s.pending = append([]byte(nil), buf[i:]...)
				return out, false
			}
			return append(out, buf[i:]...), false
		}
		if isDetach(seq) {
			return out, true
		}
		out = append(out, seq...)
		i += len(seq) - 1
	}
	return out, false
}

// csiSequence returns the CSI sequence at the start of b, which begins ESC [,
// and whether it is complete. A CSI sequence ends at a byte in 0x40-0x7e.
func csiSequence(b []byte) ([]byte, bool) {
	for i := 2; i < len(b); i++ {
		if b[i] >= 0x40 && b[i] <= 0x7e {
			return b[:i+1], true
		}
		if i >= csiLimit {
			return b[:i+1], true // too long to be one of ours; pass it on
		}
	}
	return b, false
}

// isDetach reports whether a complete CSI sequence is Ctrl-] pressed, in the
// Kitty keyboard protocol's form: CSI keycode[:alternates] ; modifiers[:event] u
func isDetach(seq []byte) bool {
	if len(seq) < 4 || seq[len(seq)-1] != 'u' {
		return false
	}
	fields := strings.Split(string(seq[2:len(seq)-1]), ";")
	if len(fields) < 2 {
		return false
	}
	key, _, _ := strings.Cut(fields[0], ":") // alternate key codes, if reported
	if key != "93" {                         // ']'
		return false
	}
	mods, event, _ := strings.Cut(fields[1], ":")
	if event != "" && event != "1" {
		return false // 2 is a repeat, 3 a release: neither is a fresh press
	}
	m, err := strconv.Atoi(mods)
	if err != nil {
		return false
	}
	return (m-1)&4 != 0 // the ctrl bit
}

// couldBeDetach reports whether an incomplete sequence might still turn into
// the detach key once the rest of it arrives.
func couldBeDetach(b []byte) bool {
	const prefix = "93;"
	rest := string(b[2:])
	if len(rest) < len(prefix) {
		return strings.HasPrefix(prefix, rest)
	}
	if !strings.HasPrefix(rest, prefix) || len(rest) >= csiLimit {
		return false
	}
	for _, c := range rest[len(prefix):] {
		if (c < '0' || c > '9') && c != ':' {
			return false
		}
	}
	return true
}
