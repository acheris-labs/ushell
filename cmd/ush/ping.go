package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"ush/internal/config"
)

const pingTimeout = 5 * time.Second

type pingPlan struct{ profile string }

// parsePing takes the profile. There is one path to check, so no modes.
func parsePing(args []string, getenv func(string) string) (pingPlan, error) {
	var p pingPlan
	fs := flag.NewFlagSet("ush ping", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&p.profile, "profile", "", "")
	if err := fs.Parse(args); err != nil {
		return p, err
	}
	if fs.NArg() > 0 {
		return p, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if p.profile == "" {
		p.profile = getenv("USH_PROFILE")
	}
	return p, nil
}

// runPing checks that this machine can log in to the rendezvous. Exit 0 only
// if that check passed.
func runPing(args []string, out io.Writer) int {
	plan, err := parsePing(args, os.Getenv)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "usage: ush [--profile NAME] ping\n\n  logs in to the rendezvous with the profile's key and lists the sessions")
		return 0
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ush ping: %v\n", err)
		return 2
	}
	profile := plan.profile

	cfg, err := loadConfig(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ush: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "profile  %s → %s\n", cfg.Profile, cfg.Rendezvous)
	fmt.Fprintf(out, "machine  %s\n", cfg.Machine)

	signer, err := loadSigner(cfg.Key)
	if err != nil {
		fmt.Fprintf(out, "key      %s  FAILED: %v\n", cfg.Key, err)
		return 1
	}
	fmt.Fprintf(out, "key      %s  %s\n", cfg.Key, ssh.FingerprintSHA256(signer.PublicKey()))

	addr := net.JoinHostPort(cfg.Rendezvous, cfg.Port)
	msg, err := pingRendezvous(cfg, signer, addr)
	if err != nil {
		fmt.Fprintf(out, "rendezvous  %s  FAILED: %s\n", addr, explain(err))
		return 1
	}
	fmt.Fprintf(out, "rendezvous  %s  ok, %s\n", addr, msg)
	return 0
}

// pingRendezvous logs in with the profile's key and runs `list`, which proves
// the port, the key and the daemon all work.
func pingRendezvous(cfg config.Config, signer ssh.Signer, addr string) (string, error) {
	sessions, elapsed, err := listRendezvous(cfg, signer, addr)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%dms, %d sessions live", elapsed.Milliseconds(), len(sessions)), nil
}

// listedSession is the part of a `list` entry the client reads.
type listedSession struct {
	ID      string `json:"session_id"`
	Name    string `json:"name"`
	Machine string `json:"machine"`
	User    string `json:"user"`
	// StartedEpoch is when the share began, in Unix seconds.
	StartedEpoch int64 `json:"started_epoch"`
}

// listRendezvous logs in with the profile's key and runs `list`. It also
// says how long logging in took.
func listRendezvous(cfg config.Config, signer ssh.Signer, addr string) ([]listedSession, time.Duration, error) {
	hostKeys, err := hostKeyCallback(cfg)
	if err != nil {
		return nil, 0, err
	}
	start := time.Now()
	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKeys,
		Timeout:         pingTimeout,
	})
	if err != nil {
		return nil, 0, err
	}
	defer client.Close()
	elapsed := time.Since(start)

	session, err := client.NewSession()
	if err != nil {
		return nil, 0, err
	}
	defer session.Close()
	raw, err := session.Output("list")
	if err != nil {
		return nil, 0, fmt.Errorf("logged in, but the rendezvous did not answer `list`: %w", err)
	}
	var sessions []listedSession
	if err := json.Unmarshal(raw, &sessions); err != nil {
		return nil, 0, fmt.Errorf("logged in, but `list` returned something unexpected: %q", strings.TrimSpace(string(raw)))
	}
	return sessions, elapsed, nil
}

// explain turns a connection error into what the user should look at.
func explain(err error) string {
	var op *net.OpError
	msg := err.Error()
	switch {
	case errors.As(err, &op) && op.Op == "dial":
		return fmt.Sprintf("unreachable (%v): wrong host or port, the rendezvous is down, or the port is not exposed", op.Err)
	case strings.Contains(msg, "unable to authenticate"):
		return "your key was rejected: it is not the rendezvous's key. Compare the fingerprint above with the rendezvous log"
	case errors.Is(err, io.EOF) || msg == "EOF":
		return "the rendezvous closed the connection (restarting, or redeployed)"
	case strings.Contains(msg, "mismatch") || strings.Contains(msg, "changed"):
		return "the rendezvous's host key changed since this machine first connected: " + msg
	default:
		return msg
	}
}
