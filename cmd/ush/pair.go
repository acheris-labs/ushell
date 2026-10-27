package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/mdp/qrterminal/v3"
	"golang.org/x/term"

	"ush/internal/config"
	"ush/internal/psk"
)

// pairing is what the QR code carries: everything the app needs to reach one
// rendezvous. Laptop paths and sharing settings are left out; the app derives
// the host key from the seed, so there is nothing to check by hand.
type pairing struct {
	V    int    `json:"v"`
	Name string `json:"name"`
	Host string `json:"host"`
	Port int    `json:"port"`
	User string `json:"user"`
	Seed string `json:"seed"`
}

func pairPayload(cfg config.Config) (string, error) {
	seed, err := psk.SeedFromKeyFile(cfg.Key)
	if err != nil {
		return "", err
	}
	port, err := strconv.Atoi(cfg.Port)
	if err != nil {
		return "", fmt.Errorf("port %q: %w", cfg.Port, err)
	}
	b, err := json.Marshal(pairing{V: 1, Name: cfg.Profile, Host: cfg.Rendezvous, Port: port,
		User: cfg.User, Seed: psk.EncodeSeed(seed)})
	return string(b), err
}

func runPair(args []string) int {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	profile := fs.String("profile", "", "profile to pair (default: the config's default)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	cfg, err := loadConfig(*profile)
	if err == nil {
		err = pair(cfg, os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ush: %v\n", err)
		return 1
	}
	return 0
}

// pair shows the profile as a QR code on the alternate screen until a key is
// pressed. The code holds the seed, which joins every session, so it is only
// ever drawn to a terminal and never left in scrollback.
func pair(cfg config.Config, in, out *os.File) error {
	if !term.IsTerminal(int(out.Fd())) || !term.IsTerminal(int(in.Fd())) {
		return errors.New("pair shows the key to every session; run it in a terminal, not a pipe or a file")
	}
	payload, err := pairPayload(cfg)
	if err != nil {
		return err
	}
	fmt.Fprint(out, "\x1b[?1049h\x1b[H\x1b[2J")
	defer fmt.Fprint(out, "\x1b[H\x1b[2J\x1b[?1049l")
	fmt.Fprintf(out, "Scan with the ush app to add %q (%s:%s).\n", cfg.Profile, cfg.Rendezvous, cfg.Port)
	fmt.Fprint(out, "Anyone who sees this code can join every session. Press any key to clear it.\n\n")
	qrterminal.GenerateHalfBlock(payload, qrterminal.L, out)

	state, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return err
	}
	defer term.Restore(int(in.Fd()), state)
	_, _ = in.Read(make([]byte, 1))
	return nil
}
