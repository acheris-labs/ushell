// ushd is the rendezvous: one port, one process, one config surface.
//
// It terminates SSH itself, so there is no sshd, no authorized_keys and no
// forced command; it keeps the session registry; and it runs the menu and the
// join terminal on a real pty by re-executing itself for each session.
// Sharers hold their share connection open; ushd opens a channel on it for
// each viewer and splices it to the viewer's own connection. Session children
// join ushd over a unix socket, attach.sock, in the run directory.
//
// Everything is derived from one seed, so an image rebuild or a redeploy
// changes no key and clients' known_hosts entries keep working.
package main

import (
	"crypto/ed25519"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"

	"ush/internal/psk"
	"ush/internal/rendezvous"
)

func main() {
	// The daemon re-executes itself to run one session on a pty.
	if len(os.Args) > 1 && os.Args[1] == rendezvous.SessionSubcommand {
		os.Exit(rendezvous.RunSession(os.Args[2:]))
	}
	os.Exit(run())
}

func run() int {
	listen := flag.String("listen", ":7874", "public address")
	runDir := flag.String("run-dir", "/run/ush", "where the registry, viewer marks and derived keys live")
	user := flag.String("user", "ush", "the only username that may log in")
	seedFile := flag.String("seed-file", "", "read the seed from this file instead of $USH_SEED")
	flag.Parse()

	seed, err := readSeed(*seedFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ushd: %v\n", err)
		return 1
	}

	keys, err := deriveKeys(seed, *runDir)
	// Keep the seed out of the environment of the session children, which
	// would otherwise inherit it. This is not secrecy from anything running
	// as this user: the shared key in runDir is the seed in another form.
	os.Unsetenv("USH_SEED")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ushd: %v\n", err)
		return 1
	}

	if err := freshState(*runDir); err != nil {
		fmt.Fprintf(os.Stderr, "ushd: %v\n", err)
		return 1
	}

	d := &rendezvous.Daemon{
		Listen:  *listen,
		Attach:  filepath.Join(*runDir, "attach.sock"),
		Dir:     filepath.Join(*runDir, "sessions"),
		Viewers: filepath.Join(*runDir, "viewers"),
		Key:     filepath.Join(*runDir, "psk"),
		HostKey: keys.host,
		Login:   keys.login,
		User:    *user,
	}
	if d.Self, err = os.Executable(); err != nil {
		fmt.Fprintf(os.Stderr, "ushd: %v\n", err)
		return 1
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ushd: %v\n", err)
		return 1
	}
	log.Printf("ushd: listening on %s", *listen)
	if err := d.Serve(ln); err != nil {
		fmt.Fprintf(os.Stderr, "ushd: %v\n", err)
		return 1
	}
	return 0
}

// readSeed takes the seed from a file if one is named, and from the
// environment otherwise. A file is the better default for a deployment: an
// environment variable stays readable in /proc/self/environ and in any dump.
func readSeed(path string) ([]byte, error) {
	text := os.Getenv("USH_SEED")
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		text = string(b)
	}
	if text == "" {
		return nil, fmt.Errorf("no seed: set USH_SEED or pass -seed-file (run: make seed)")
	}
	return psk.ParseSeed(text)
}

type derived struct {
	host  ed25519.PrivateKey // what clients see on the public port
	login ssh.PublicKey      // the only key that may log in
}

// deriveKeys works out every key from the seed and writes the one file
// something other than this process reads: the shared key, which a session
// child logs in with. Fingerprints are logged because that is how a seed
// mismatch is diagnosed, and two of the client's error messages point at
// them.
func deriveKeys(seed []byte, runDir string) (derived, error) {
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return derived{}, err
	}

	login := psk.FromSeed(seed)
	// The label stays "sshd" even though sshd is gone: it is an input to the
	// derivation, so changing it would change the host key and every client's
	// known_hosts entry would stop matching.
	host := psk.HostKey(seed, "sshd")

	if _, err := psk.WriteComment(filepath.Join(runDir, "psk"), login, "ush-psk"); err != nil {
		return derived{}, err
	}

	pub, err := ssh.NewPublicKey(login.Public())
	if err != nil {
		return derived{}, err
	}
	hostPub, err := ssh.NewPublicKey(host.Public())
	if err != nil {
		return derived{}, err
	}
	log.Printf("ushd: %-16s %s", "shared key", ssh.FingerprintSHA256(pub))
	log.Printf("ushd: %-16s %s", "sshd host key", ssh.FingerprintSHA256(hostPub))

	return derived{host: host, login: pub}, nil
}

// freshState clears what the last run left behind. Nothing here survives a
// restart by design: a registration lives exactly as long as its connection,
// so entries from a process that is gone are phantoms.
func freshState(runDir string) error {
	sessions := filepath.Join(runDir, "sessions")
	if err := os.RemoveAll(sessions); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(runDir, "viewers")); err != nil {
		return err
	}
	return os.MkdirAll(sessions, 0o755)
}
