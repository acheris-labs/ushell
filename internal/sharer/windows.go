package sharer

import (
	"sync"

	"ush/internal/tunnel"
)

// windows is every terminal looking at the shared pty, by id, and the size
// the pty should be: the smallest of them in each dimension, so no terminal
// ever sees a screen wider or taller than itself. Terminals with a zero
// dimension (no size known) do not count.
type windows struct {
	mu    sync.Mutex
	sizes map[string]tunnel.Size
}

func newWindows() *windows {
	return &windows{sizes: make(map[string]tunnel.Size)}
}

func (w *windows) set(id string, size tunnel.Size) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sizes[id] = size
}

func (w *windows) remove(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.sizes, id)
}

// min is the size the pty should be, and false if nothing has a size.
func (w *windows) min() (tunnel.Size, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out tunnel.Size
	for _, s := range w.sizes {
		if s.Cols == 0 || s.Rows == 0 {
			continue
		}
		if out.Cols == 0 || s.Cols < out.Cols {
			out.Cols = s.Cols
		}
		if out.Rows == 0 || s.Rows < out.Rows {
			out.Rows = s.Rows
		}
	}
	return out, out.Cols != 0
}
