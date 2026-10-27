package main

import (
	"errors"
	"net"
	"strings"
	"syscall"
	"testing"
)

func TestExplainConnectionErrors(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	cases := map[string]struct {
		err  error
		want string
	}{
		"refused": {refused, "unreachable"},
		"wrong key": {errors.New("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey], no supported methods remain"),
			"key was rejected"},
		"host key changed": {errors.New("ssh: handshake failed: knownhosts: key mismatch"), "host key changed"},
		"other":            {errors.New("something odd"), "something odd"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := explain(c.err); !strings.Contains(got, c.want) {
				t.Fatalf("explain(%v) = %q, want it to mention %q", c.err, got, c.want)
			}
		})
	}
}

func TestPingTakesOnlyAProfile(t *testing.T) {
	p, err := parsePing(nil, func(string) string { return "home" })
	if err != nil || p.profile != "home" {
		t.Fatalf("no flags: %+v, %v", p, err)
	}
	p, _ = parsePing([]string{"--profile", "work"}, func(string) string { return "home" })
	if p.profile != "work" {
		t.Errorf("--profile should beat $USH_PROFILE, got %q", p.profile)
	}
	for _, old := range [][]string{{"-s"}, {"--share"}, {"-c"}, {"--connect"}} {
		if _, err := parsePing(old, func(string) string { return "" }); err == nil {
			t.Errorf("%v: there is one path now; the old mode flags must be errors", old)
		}
	}
}
