//go:build !darwin

package sharer

import (
	"errors"
	"os"

	"ush/internal/tunnel"
)

func adopt(int, tunnel.Size, bool) (*os.File, error) {
	return nil, errors.New("adopting a running process is only supported on macOS")
}

func waitPid(int) error { return nil }
