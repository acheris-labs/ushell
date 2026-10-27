// Package hostkey verifies the rendezvous's host key the way OpenSSH's
// StrictHostKeyChecking=accept-new does: an unknown host is recorded and
// trusted, a known host must present the recorded key.
package hostkey

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// TOFU returns a callback over the known_hosts file at path, creating it
// (and its directory) if missing. An unknown host's key is appended and
// accepted; a known host offering a different key is refused with the file
// and line of the recorded one. Only the host name is recorded, never the
// address, so a rendezvous that moves keeps its entry.
//
// The file is only ever opened for append, and only when a host actually
// needs recording: a known host, however it is already known, never has its
// file reopened for writing. That keeps a read-only known_hosts (a
// home-manager/Nix symlink into the store, or a plain chmod 444) usable for
// every host already in it.
func TOFU(path string) (ssh.HostKeyCallback, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		_ = f.Close()
	} else if err != nil {
		return nil, err
	}
	if _, err := knownhosts.New(path); err != nil {
		return nil, err // unreadable, or a line it cannot parse: say so now
	}
	var mu sync.Mutex
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		// One callback serves every reconnect of a share, so the file is
		// read on every call: what the last call recorded must count now,
		// and a key that changes mid-session must be refused, not appended.
		mu.Lock()
		defer mu.Unlock()
		known, err := knownhosts.New(path)
		if err != nil {
			return err
		}
		err = known(hostname, remote, key)
		var keyErr *knownhosts.KeyError
		if !errors.As(err, &keyErr) {
			return err // nil, or something other than "unknown or changed"
		}
		if len(keyErr.Want) > 0 {
			w := keyErr.Want[0]
			return fmt.Errorf("the host key of %s changed: it offers %s but %s:%d records %s; "+
				"remove that line if the rendezvous was redeployed with a new seed",
				hostname, ssh.FingerprintSHA256(key), w.Filename, w.Line, ssh.FingerprintSHA256(w.Key))
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_RDWR, 0o600)
		if err != nil {
			return fmt.Errorf("recording the new host in %s: %w", path, err)
		}
		defer f.Close()
		if err := ensureTrailingNewline(f); err != nil {
			return fmt.Errorf("recording the new host in %s: %w", path, err)
		}
		_, err = fmt.Fprintln(f, knownhosts.Line([]string{hostname}, key))
		return err
	}, nil
}

// ensureTrailingNewline writes a '\n' to f, open for append, if its last
// byte isn't already one. A hand-edited known_hosts missing its final
// newline would otherwise have the new entry glued onto the last line,
// corrupting both.
func ensureTrailingNewline(f *os.File) error {
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if size == 0 {
		return nil
	}
	buf := make([]byte, 1)
	if _, err := f.ReadAt(buf, size-1); err != nil {
		return err
	}
	if buf[0] == '\n' {
		return nil
	}
	_, err = f.WriteString("\n")
	return err
}
