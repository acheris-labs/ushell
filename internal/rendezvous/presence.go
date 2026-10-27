package rendezvous

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// Most viewers reach a session through a session child on the rendezvous,
// marked here, so the count is usually exact. A viewer that opens a viewer
// channel directly on the public port (Daemon.viewer, bypassing the menu)
// takes no mark, so the count can undercount those.

// markViewer records this process as watching session id, and returns how to
// take the mark down.
func markViewer(dir, id string) (func(), error) {
	d := filepath.Join(dir, id)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return nil, err
	}
	mark := filepath.Join(d, strconv.Itoa(os.Getpid()))
	if err := os.WriteFile(mark, nil, 0o644); err != nil {
		return nil, err
	}
	return func() {
		os.Remove(mark)
		os.Remove(d) // only succeeds once the last viewer leaves
	}, nil
}

// countViewers counts marks whose process is still alive; a killed session
// cannot take its mark down, so dead ones are ignored.
func countViewers(dir, id string) int {
	if dir == "" {
		return 0
	}
	entries, _ := os.ReadDir(filepath.Join(dir, id))
	n := 0
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err == nil && syscall.Kill(pid, 0) == nil {
			n++
		}
	}
	return n
}
