package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"

	"ush/internal/config"
	"ush/internal/psk"
)

const initUsage = `usage: ush init [--profile NAME] [--rendezvous HOST] [--port N]
                [--key PATH] [--default]
                [--skip-host-key-check] [--seed] [-y]

Adds a profile to ~/.ushrc (or $USH_RC), creating the file if needed, and
generates the key if it does not exist. Asks for anything not given as a flag;
-y accepts the defaults instead of asking.

With --seed, the key is derived from the shared seed instead: the 52-character
USH_SEED value the rendezvous runs with (see its .env). It is read from
$USH_SEED or asked for. To make a brand-new seed, use ush seed.
`

type initArgs struct {
	profile          string
	rendezvous       string
	port             int
	key              string
	makeDefault      bool
	skipHostKeyCheck bool
	seed             bool
	yes              bool

	// tty is stdin when it is an interactive terminal, so secrets can be read
	// without echo. Nil otherwise, including in tests.
	tty *os.File
}

func runInit(args []string) int {
	var a initArgs
	fs := flag.NewFlagSet("ush init", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, initUsage) }
	fs.StringVar(&a.profile, "profile", "", "")
	fs.StringVar(&a.rendezvous, "rendezvous", "", "")
	fs.IntVar(&a.port, "port", 0, "")
	fs.StringVar(&a.key, "key", "", "")
	fs.BoolVar(&a.makeDefault, "default", false, "")
	fs.BoolVar(&a.skipHostKeyCheck, "skip-host-key-check", false, "")
	fs.BoolVar(&a.seed, "seed", false, "")
	fs.BoolVar(&a.yes, "y", false, "")
	fs.BoolVar(&a.yes, "yes", false, "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "ush init: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		a.tty = os.Stdin
	}
	if err := initConfig(a, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "ush init: %v\n", err)
		return 1
	}
	return 0
}

func initConfig(a initArgs, stdin io.Reader, out io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	rc := os.Getenv("USH_RC")
	if rc == "" {
		rc = filepath.Join(home, ".ushrc")
	}
	original, err := os.ReadFile(rc)
	existed := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	def, names, err := config.Inspect(string(original))
	if err != nil {
		return fmt.Errorf("%s does not parse: %w", rc, err)
	}

	if slices.Contains(names, a.profile) {
		return fmt.Errorf("%s already has a profile %q", rc, a.profile)
	}
	in := bufio.NewReader(stdin)
	if a, err = gather(a, in, out); err != nil {
		return err
	}
	if slices.Contains(names, a.profile) {
		return fmt.Errorf("%s already has a profile %q", rc, a.profile)
	}
	if def != "" && !a.makeDefault && !a.yes {
		answer, err := ask(in, out, fmt.Sprintf("Make %q the default instead of %q? [y/N]", a.profile, def), "")
		if err != nil {
			return err
		}
		a.makeDefault = strings.HasPrefix(strings.ToLower(answer), "y")
	}

	keyPath := config.ExpandHome(a.key, home)
	var created bool
	if a.seed {
		text, err := readSeed(a, in, out, os.Getenv)
		if err != nil {
			return err
		}
		seed, err := psk.ParseSeed(text)
		if err != nil {
			return err
		}
		if created, err = psk.Write(keyPath, psk.FromSeed(seed)); err != nil {
			return err
		}
	} else if created, err = ensureKey(keyPath); err != nil {
		return err
	}

	text, err := config.AddProfile(string(original), a.profile, config.Profile{
		Rendezvous:       a.rendezvous,
		Port:             a.port,
		Key:              a.key,
		SkipHostKeyCheck: a.skipHostKeyCheck,
	}, a.makeDefault)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(rc, []byte(text), 0o600); err != nil {
		return err
	}
	// Belt and braces: the file must load through the real loader, or it is
	// put back exactly as it was.
	if _, err := config.Load(rc, home, a.profile); err != nil {
		if existed {
			_ = writeFileAtomic(rc, original, 0o600)
		} else {
			_ = os.Remove(rc)
		}
		return fmt.Errorf("the new config did not load, so it was rolled back: %w", err)
	}

	isDefault := def == "" || a.makeDefault
	fmt.Fprintf(out, "\nAdded profile %q to %s", a.profile, rc)
	if isDefault {
		fmt.Fprint(out, " (the default)")
	}
	fmt.Fprintln(out, ".")
	pub, _ := os.ReadFile(keyPath + ".pub")
	switch {
	case a.seed && created:
		fmt.Fprintf(out, "Derived %s from the seed.\n", keyPath)
	case a.seed:
		fmt.Fprintf(out, "%s already holds the key for this seed.\n", keyPath)
	case created:
		fmt.Fprintf(out, "Generated %s and %s.pub.\n", keyPath, keyPath)
	default:
		fmt.Fprintf(out, "Using the existing key %s.\n", keyPath)
	}
	fmt.Fprintf(out, "Key fingerprint: %s\n", fingerprint(pub))

	if a.seed {
		fmt.Fprintln(out, "The rendezvous logs its own fingerprint at startup; they match when the seeds do.")
	} else {
		fmt.Fprintf(out, `
This key is the shared key for the whole setup. Every other machine that
shares or connects needs a copy of %s.
`, keyPath)
	}
	return nil
}

// readSeed takes the seed from $USH_SEED, or asks for it, reading the answer
// without echo on a terminal. It never invents one: an empty answer used to
// generate a new seed, which quietly produced the wrong key for anyone who
// meant to paste. New seeds come from `ush seed` or `make seed`.
func readSeed(a initArgs, in io.Reader, out io.Writer, getenv func(string) string) (string, error) {
	const missing = "no seed given: paste the USH_SEED value from the rendezvous's .env (a new one comes from `ush seed`)"
	r, ok := in.(*bufio.Reader)
	if !ok {
		r = bufio.NewReader(in)
	}
	if s := strings.TrimSpace(getenv("USH_SEED")); s != "" {
		_, err := psk.ParseSeed(s)
		return s, err
	}
	if a.yes {
		return "", errors.New(missing)
	}

	fmt.Fprint(out, "Seed (the USH_SEED value; typing is hidden): ")
	var answer string
	// Read straight from the terminal only if nothing was typed ahead into
	// the buffer, or those bytes would be skipped.
	if a.tty != nil && r.Buffered() == 0 {
		b, err := term.ReadPassword(int(a.tty.Fd()))
		fmt.Fprintln(out)
		if err != nil {
			return "", err
		}
		answer = string(b)
	} else {
		line, err := r.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		answer = line
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return "", errors.New(missing)
	}
	if _, err := psk.ParseSeed(answer); err != nil {
		return "", err
	}
	fmt.Fprintf(out, "seed read (%d characters)\n", len(answer))
	return answer, nil
}

func fingerprint(pubLine []byte) string {
	k, _, _, _, err := ssh.ParseAuthorizedKey(pubLine)
	if err != nil {
		return "unknown"
	}
	return ssh.FingerprintSHA256(k)
}

// gather fills in whatever the flags left unset: by asking, or with -y by
// taking the defaults. The rendezvous has no default.
func gather(a initArgs, in io.Reader, out io.Writer) (initArgs, error) {
	r, ok := in.(*bufio.Reader)
	if !ok {
		r = bufio.NewReader(in)
	}
	prompt := func(dst *string, label, fallback string) error {
		if *dst != "" {
			return nil
		}
		if a.yes {
			*dst = fallback
			return nil
		}
		v, err := ask(r, out, label, fallback)
		*dst = v
		return err
	}
	promptPort := func(dst *int, label string, fallback int) error {
		if *dst != 0 {
			return validPort(label, *dst)
		}
		s := ""
		if err := prompt(&s, label, strconv.Itoa(fallback)); err != nil {
			return err
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("%s %q is not a number", strings.ToLower(label), s)
		}
		*dst = n
		return validPort(label, n)
	}

	if err := prompt(&a.profile, "Profile name", "default"); err != nil {
		return a, err
	}
	if err := prompt(&a.rendezvous, "Rendezvous host", ""); err != nil {
		return a, err
	}
	if a.rendezvous == "" {
		return a, errors.New("a rendezvous host is required (--rendezvous HOST)")
	}
	if err := promptPort(&a.port, "Rendezvous port", 7874); err != nil {
		return a, err
	}
	if err := prompt(&a.key, "Save the key file to", "~/.ssh/ush_ed25519"); err != nil {
		return a, err
	}
	return a, nil
}

// ask prints "label [fallback]: " and returns the trimmed answer, or fallback
// for an empty answer or end of input.
func ask(r *bufio.Reader, out io.Writer, label, fallback string) (string, error) {
	if fallback != "" {
		fmt.Fprintf(out, "%s [%s]: ", label, fallback)
	} else {
		fmt.Fprintf(out, "%s: ", label)
	}
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if v := strings.TrimSpace(line); v != "" {
		return v, nil
	}
	return fallback, nil
}

func validPort(label string, n int) error {
	if n < 1 || n > 65535 {
		return fmt.Errorf("%s %d is not a valid port", strings.ToLower(label), n)
	}
	return nil
}

// ensureKey creates a random keypair at path unless one exists, and writes a
// missing .pub from an existing private key. It never overwrites a key.
func ensureKey(path string) (created bool, err error) {
	priv, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		seed, err := psk.NewSeed()
		if err != nil {
			return false, err
		}
		raw, err := psk.ParseSeed(seed)
		if err != nil {
			return false, err
		}
		return psk.Write(path, psk.FromSeed(raw))
	}
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path + ".pub"); err == nil {
		return false, nil
	}
	signer, err := ssh.ParsePrivateKey(priv)
	if err != nil {
		return false, fmt.Errorf("%s exists but its public key is missing and cannot be derived: %w", path, err)
	}
	line := append(bytes.TrimRight(ssh.MarshalAuthorizedKey(signer.PublicKey()), "\n"), " ush-psk\n"...)
	return false, os.WriteFile(path+".pub", line, 0o644)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ushrc-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
