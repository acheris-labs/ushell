package rendezvous

import (
	"fmt"
	"net"
	"sync"
	"time"
)

// limitListener bounds the connections that have not authenticated yet,
// overall and per address: OpenSSH's MaxStartups. A connection takes a slot
// when accepted and gives it back when it opens a session or channel (the
// handler frees it), when it closes, or once the handshake timeout has passed,
// whichever is first. By then a connection that has not authenticated has been
// cut off; one that has, but opened nothing, must not keep the slot forever.
type limitListener struct {
	net.Listener
	logf    func(string, ...any)
	waiting waiting
	refused every
	release time.Duration // how long a connection may hold its slot at most
}

func newLimitListener(ln net.Listener, logf func(string, ...any)) *limitListener {
	return &limitListener{Listener: ln, logf: logf, refused: every{period: logEvery}, release: handshakeTimeout}
}

func (l *limitListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ip := remoteIP(c)
		if !l.waiting.take(ip) {
			// Far more half-open connections than anyone could be using.
			_ = c.Close()
			l.refused.logf(l.logf, "ushd: %s: refused, too many connections waiting", ip)
			continue
		}
		sc := &slotConn{Conn: c, free: sync.OnceFunc(func() { l.waiting.done(ip) })}
		time.AfterFunc(l.release, sc.free)
		return sc, nil
	}
}

// slotConn carries the connection's waiting slot, freed once it
// authenticates or closes, whichever comes first.
type slotConn struct {
	net.Conn
	free func() // safe to call more than once
}

func (c *slotConn) Close() error {
	c.free()
	return c.Conn.Close()
}

// slotKey is where the SSH context keeps the *slotConn, for the handler.
type slotKey struct{}

// waiting counts connections that have not authenticated yet, overall and
// per address.
type waiting struct {
	mu    sync.Mutex
	total int
	by    map[string]int
}

func (w *waiting) take(ip string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.total >= maxPending || w.by[ip] >= maxPendingPerIP {
		return false
	}
	if w.by == nil {
		w.by = make(map[string]int)
	}
	w.total++
	w.by[ip]++
	return true
}

func (w *waiting) done(ip string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.total--
	if w.by[ip]--; w.by[ip] == 0 {
		delete(w.by, ip)
	}
}

func remoteIP(c net.Conn) string {
	addr := c.RemoteAddr().String()
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// every logs at most once per period, counting the lines it swallowed.
type every struct {
	period time.Duration

	mu      sync.Mutex
	last    time.Time
	dropped int
}

func (e *every) logf(logf func(string, ...any), format string, args ...any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if time.Since(e.last) < e.period {
		e.dropped++
		return
	}
	if e.dropped > 0 {
		format += fmt.Sprintf(" (and %d more since the last report)", e.dropped)
	}
	logf(format, args...)
	e.last, e.dropped = time.Now(), 0
}
