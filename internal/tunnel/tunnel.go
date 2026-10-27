// Package tunnel is the wire protocol between a sharer, the rendezvous, and a
// viewer: one SSH channel type, carrying a terminal.
//
// The rendezvous opens the channel on the sharer's own connection, and the
// session child on the rendezvous opens it on its connection to the
// rendezvous, which splices the two. On the channel, the viewer asks for a
// terminal with a standard pty-req (RFC 4254 §6.2) and reports resizes with
// window-change (§6.7); after that the channel's data is the terminal, both
// ways. Either side ends the viewer by closing the channel.
package tunnel

import (
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/ssh"
)

// ChannelType names the channel. Local channel types carry a domain
// (RFC 4250 §4.6.1).
const ChannelType = "viewer@ush.acheris.net"

// Open is the channel's extra data when a viewer opens it towards the
// rendezvous: which session it wants. The rendezvous opens it towards the
// sharer with no extra data; the connection already says which session.
type Open struct {
	Session string
}

// Size is a terminal's size in cells.
type Size struct {
	Cols, Rows uint32
}

// ptyRequest is the pty-req payload, RFC 4254 §6.2.
type ptyRequest struct {
	Term          string
	Cols, Rows    uint32
	Width, Height uint32 // pixels; always 0 here
	Modes         string // encoded terminal modes; always empty here
}

// windowChange is the window-change payload, RFC 4254 §6.7.
type windowChange struct {
	Cols, Rows    uint32
	Width, Height uint32
}

// ErrRefused is returned by Dial when the sharer answers the pty-req with a
// refusal.
var ErrRefused = errors.New("the sharer refused the terminal")

// acceptTimeout bounds how long Accept waits for the pty-req. A real viewer
// sends it at once; anything else must not hold a goroutine forever.
const acceptTimeout = 10 * time.Second

// Viewer is the viewer's end of a channel.
type Viewer struct {
	ssh.Channel
}

// Dial opens a channel for session on conn and asks for a terminal. On the
// rendezvous side of a Splice the session is what it routes on; the sharer
// ignores it.
func Dial(conn ssh.Conn, session, term string, size Size) (*Viewer, error) {
	ch, reqs, err := conn.OpenChannel(ChannelType, ssh.Marshal(Open{Session: session}))
	if err != nil {
		var open *ssh.OpenChannelError
		if errors.As(err, &open) {
			return nil, fmt.Errorf("rendezvous refused the viewer: %s", open.Message)
		}
		return nil, err
	}
	go ssh.DiscardRequests(reqs)
	ok, err := ch.SendRequest("pty-req", true, ssh.Marshal(ptyRequest{Term: term, Cols: size.Cols, Rows: size.Rows}))
	if err != nil {
		_ = ch.Close()
		return nil, err
	}
	if !ok {
		_ = ch.Close()
		return nil, ErrRefused
	}
	return &Viewer{Channel: ch}, nil
}

// Resize tells the sharer the viewer's terminal changed size.
func (v *Viewer) Resize(size Size) error {
	_, err := v.SendRequest("window-change", false, ssh.Marshal(windowChange{Cols: size.Cols, Rows: size.Rows}))
	return err
}

// Guest is the sharer's end of a channel, after the viewer has asked for a
// terminal.
type Guest struct {
	ssh.Channel
	Term string
	Size Size
}

// Accept takes a channel the rendezvous opened and waits for the viewer's
// pty-req, which fills in Term and Size. Later window-change requests call
// resize, from a goroutine of their own, until the channel closes. A viewer
// that sends no pty-req within acceptTimeout, or hangs up first, is refused.
func Accept(nc ssh.NewChannel, resize func(Size)) (*Guest, error) {
	ch, reqs, err := nc.Accept()
	if err != nil {
		return nil, err
	}
	g := &Guest{Channel: ch}
	ready := make(chan error, 1)
	go func() {
		got := false
		for r := range reqs {
			switch r.Type {
			case "pty-req":
				var p ptyRequest
				if err := ssh.Unmarshal(r.Payload, &p); err != nil || got {
					_ = r.Reply(false, nil)
					continue
				}
				got = true
				g.Term, g.Size = p.Term, Size{Cols: p.Cols, Rows: p.Rows}
				_ = r.Reply(true, nil)
				ready <- nil
			case "window-change":
				var w windowChange
				if err := ssh.Unmarshal(r.Payload, &w); err == nil && got {
					resize(Size{Cols: w.Cols, Rows: w.Rows})
				}
				_ = r.Reply(false, nil) // a no-op unless the viewer asked for one
			default:
				_ = r.Reply(false, nil)
			}
		}
		if !got {
			ready <- io.EOF
		}
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = ch.Close()
			return nil, fmt.Errorf("viewer hung up before asking for a terminal: %w", err)
		}
		return g, nil
	case <-time.After(acceptTimeout):
		_ = ch.Close()
		return nil, errors.New("viewer never asked for a terminal")
	}
}

// Splice joins two channels: every request on one is sent on the other, with
// the reply carried back, and bytes are copied both ways. It returns once
// either side finishes, having closed both.
func Splice(a ssh.Channel, aReqs <-chan *ssh.Request, b ssh.Channel, bReqs <-chan *ssh.Request) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go forward(b, aReqs)
	go forward(a, bReqs)
	<-done
	_ = a.Close()
	_ = b.Close()
	<-done
}

func forward(dst ssh.Channel, reqs <-chan *ssh.Request) {
	for r := range reqs {
		ok, err := dst.SendRequest(r.Type, r.WantReply, r.Payload)
		if r.WantReply {
			_ = r.Reply(ok && err == nil, nil)
		}
	}
}
