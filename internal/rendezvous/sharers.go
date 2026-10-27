package rendezvous

import (
	"sync"

	gssh "charm.land/ssh"
	"golang.org/x/crypto/ssh"

	"ush/internal/tunnel"
)

// sharers is every live share's connection, by session id: what a viewer's
// channel is opened on. An entry lives exactly as long as the share command
// that made it.
type sharers struct {
	mu    sync.Mutex
	conns map[string]*ssh.ServerConn
}

func (s *sharers) add(id string, c *ssh.ServerConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conns == nil {
		s.conns = make(map[string]*ssh.ServerConn)
	}
	s.conns[id] = c
}

func (s *sharers) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, id)
}

func (s *sharers) get(id string) *ssh.ServerConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns[id]
}

// viewer serves one viewer channel: it opens the same channel on the
// sharer's connection and splices the two, forwarding the terminal request
// and every resize, and copying bytes both ways, without reading either.
// Anyone who can open this channel holds the key, so this is join without
// the menu, at the same trust level.
func (d *Daemon) viewer(_ *gssh.Server, _ *ssh.ServerConn, nc ssh.NewChannel, ctx gssh.Context) {
	// Authenticated: this connection no longer counts against MaxStartups,
	// whether or not it ever opens a session channel.
	if c, ok := ctx.Value(slotKey{}).(*slotConn); ok {
		c.free()
	}
	var open tunnel.Open
	if err := ssh.Unmarshal(nc.ExtraData(), &open); err != nil || !validSessionID.MatchString(open.Session) {
		_ = nc.Reject(ssh.ConnectionFailed, "bad session id")
		return
	}
	sharer := d.sharers.get(open.Session)
	if sharer == nil {
		_ = nc.Reject(ssh.ConnectionFailed, "no such session")
		return
	}
	up, upReqs, err := sharer.OpenChannel(tunnel.ChannelType, nil)
	if err != nil {
		d.logf("ushd: %s: the sharer did not take a viewer: %v", open.Session, err)
		_ = nc.Reject(ssh.ConnectionFailed, "the sharer did not take a viewer")
		return
	}
	down, downReqs, err := nc.Accept()
	if err != nil {
		_ = up.Close()
		return
	}
	tunnel.Splice(down, downReqs, up, upReqs)
}
