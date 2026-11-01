package rendezvous

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"ush/internal/register"
)

// cutter forwards connections to addr until cut drops every one of them, as a
// network blip or a laptop going to sleep would.
type cutter struct {
	addr   string
	mu     sync.Mutex
	conns  []net.Conn
	paused bool // refuse new connections, as a rendezvous that is away would
}

func startCutter(t *testing.T, to string) *cutter {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	c := &cutter{addr: ln.Addr().String()}
	go func() {
		for {
			in, err := ln.Accept()
			if err != nil {
				return
			}
			c.mu.Lock()
			paused := c.paused
			c.mu.Unlock()
			if paused {
				in.Close()
				continue
			}
			out, err := net.Dial("tcp", to)
			if err != nil {
				in.Close()
				continue
			}
			c.mu.Lock()
			c.conns = append(c.conns, in, out)
			c.mu.Unlock()
			go func() { _, _ = io.Copy(out, in); out.Close() }()
			go func() { _, _ = io.Copy(in, out); in.Close() }()
		}
	}()
	return c
}

// cut drops every connection; paused keeps new ones from getting through.
func (c *cutter) cut(paused bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, conn := range c.conns {
		conn.Close()
	}
	c.conns = nil
	c.paused = paused
}

func TestAReconnectKeepsTheEdits(t *testing.T) {
	h := startDaemon(t)
	link := startCutter(t, h.addr)

	var mu sync.Mutex
	var logs strings.Builder
	logged := func(want string) bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(logs.String(), want)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	err := register.Register(ctx, register.Target{
		Addr: link.addr,
		Config: &ssh.ClientConfig{
			User:            "ush",
			Auth:            []ssh.AuthMethod{ssh.PublicKeys(h.signer)},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		},
		Info: register.Info{Name: "api", Description: "as started", Machine: "umber"},
	}, func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(&logs, format+"\n", args...)
	})
	if err != nil {
		t.Fatal(err)
	}

	only := func(cond func(session) bool, what string) session {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if all := loadSessions(h.dir); len(all) == 1 && cond(all[0]) {
				return all[0]
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("never saw %s; registry: %+v", what, loadSessions(h.dir))
		return session{}
	}
	first := only(func(s session) bool { return s.Name == "api" }, "the first registration")

	if err := describeSession(h.dir, first.ID, "pr monitor"); err != nil {
		t.Fatal(err)
	}
	if err := renameSession(h.dir, first.ID, "api-v2"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !logged(`description "pr monitor"`) || !logged(`name "api-v2"`) {
		if time.Now().After(deadline) {
			t.Fatalf("the sharer never heard about the edits; its log:\n%s", logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	link.cut(false)
	again := only(func(s session) bool { return s.ID != first.ID }, "a re-registration")
	if again.Name != "api-v2" || again.Description != "pr monitor" {
		t.Fatalf("re-registered as %q, %q; want the edits kept", again.Name, again.Description)
	}
}

func TestTheSharersEditsReachTheMenuAndOutliveAnOutage(t *testing.T) {
	h := startDaemon(t)
	link := startCutter(t, h.addr)
	label := register.NewLabel("api", "")
	var mu sync.Mutex
	var logs strings.Builder
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	err := register.Register(ctx, register.Target{
		Addr: link.addr,
		Config: &ssh.ClientConfig{
			User:            "ush",
			Auth:            []ssh.AuthMethod{ssh.PublicKeys(h.signer)},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		},
		Info:  register.Info{Name: "ignored", Machine: "umber"}, // the label wins
		Label: label,
	}, func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(&logs, format+"\n", args...)
	})
	if err != nil {
		t.Fatal(err)
	}
	listed := func(name, desc string, other string) session {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if all := loadSessions(h.dir); len(all) == 1 && all[0].ID != other &&
				all[0].Name == name && all[0].Description == desc {
				return all[0]
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("never listed as %q, %q; registry: %+v", name, desc, loadSessions(h.dir))
		return session{}
	}
	desc := "pr monitor"
	first := listed("api", "", "")
	if err := label.Set(nil, &desc); err != nil {
		t.Fatal(err)
	}
	if again := listed("api", "pr monitor", ""); again.ID != first.ID {
		t.Fatal("the edit should change the registration in place, not make a new one")
	}

	// Edited while the rendezvous cannot be reached: the next registration
	// carries it.
	link.cut(true)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		mu.Lock()
		dropped := strings.Contains(logs.String(), "registration dropped")
		mu.Unlock()
		if dropped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the sharer never noticed the drop")
		}
	}
	name := "api-v2"
	if err := label.Set(&name, nil); err != nil {
		t.Fatal(err)
	}
	link.cut(false)
	listed("api-v2", "pr monitor", first.ID)
}
