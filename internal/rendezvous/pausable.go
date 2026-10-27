package rendezvous

import (
	"bytes"
	"io"
	"sync"
)

// pausable passes session output through, except while the Ctrl-] panel is
// up, when it buffers it to replay on return so the screen stays in step.
type pausable struct {
	mu       sync.Mutex
	w        io.Writer
	paused   bool
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func newPausable(w io.Writer, limit int) *pausable {
	return &pausable{w: w, limit: limit}
}

func (p *pausable) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.paused {
		return p.w.Write(b)
	}
	if p.buf.Len()+len(b) > p.limit {
		p.buf.Reset() // too much to replay; the caller forces a redraw instead
		p.overflow = true
		return len(b), nil
	}
	p.buf.Write(b)
	return len(b), nil
}

func (p *pausable) pause() {
	p.mu.Lock()
	p.paused = true
	p.mu.Unlock()
}

// resume replays what was buffered and reports whether some was dropped.
func (p *pausable) resume() (overflowed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = p.w.Write(p.buf.Bytes())
	p.buf.Reset()
	p.paused = false
	overflowed, p.overflow = p.overflow, false
	return overflowed
}
