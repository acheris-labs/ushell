// Package keep runs a command in a pty that outlives the terminal that
// started it, and lets terminals attach to and detach from it.
//
// It is the smallest thing that makes `ush -s` survive: the rendezvous can
// restart, the sharing process can exit, and the command keeps running with
// its screen intact. tmux and herdr do this too, along with windows, panes,
// status bars and their own key bindings; this does only the part ush needs.
//
// What it does keep is the screen. A terminal emulator follows the command's
// output, so attaching repaints what is there now rather than replaying raw
// bytes and hoping the program redraws itself.
package keep

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// The wire protocol between an attached terminal and the keeper. Frames are a
// kind byte, a length, and a payload, in both directions.
type kind byte

const (
	kindOutput kind = 1 // keeper → terminal: bytes from the command
	kindInput  kind = 2 // terminal → keeper: keystrokes
	kindResize kind = 3 // terminal → keeper: a new window size
	kindStatus kind = 4 // keeper → terminal: what is running here
	kindAsk    kind = 5 // terminal → keeper: send status, then go away
	kindStop   kind = 6 // terminal → keeper: end the session
)

// maxFrame bounds one frame's payload. Terminal writes are far smaller; this
// is only here so a confused peer cannot ask for an enormous allocation.
const maxFrame = 1 << 20

var errFrameTooBig = errors.New("keep: frame too large")

func writeFrame(w io.Writer, k kind, payload []byte) error {
	if len(payload) > maxFrame {
		return errFrameTooBig
	}
	head := [5]byte{byte(k)}
	binary.BigEndian.PutUint32(head[1:], uint32(len(payload)))
	if _, err := w.Write(head[:]); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

func readFrame(r io.Reader) (kind, []byte, error) {
	var head [5]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(head[1:])
	if n > maxFrame {
		return 0, nil, errFrameTooBig
	}
	if n == 0 {
		return kind(head[0]), nil, nil
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return kind(head[0]), payload, nil
}

// Size is a terminal's dimensions, sent as four bytes.
type Size struct{ Width, Height int }

func (s Size) encode() []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b, uint16(s.Width))
	binary.BigEndian.PutUint16(b[2:], uint16(s.Height))
	return b
}

func decodeSize(b []byte) (Size, error) {
	if len(b) != 4 {
		return Size{}, fmt.Errorf("keep: malformed size frame")
	}
	return Size{
		Width:  int(binary.BigEndian.Uint16(b)),
		Height: int(binary.BigEndian.Uint16(b[2:])),
	}, nil
}
