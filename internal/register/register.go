package register

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"ush/internal/tunnel"
)

// Target is where and how to register.
type Target struct {
	Addr    string
	Config  *ssh.ClientConfig
	Command string
	// OnViewer is called for every viewer channel the rendezvous opens on
	// the registration connection, including the ones after a reconnect.
	OnViewer func(ssh.NewChannel)
}

const (
	handshakeTimeout = 15 * time.Second
	keepAlive        = 30 * time.Second
	maxBackoff       = 30 * time.Second
)

// ErrRetrying marks a first registration that failed for want of a
// rendezvous to reach: Register is already retrying it in the background.
var ErrRetrying = errors.New("retrying in the background")

// Register makes the first registration synchronously, so a failure is
// reported before the shared terminal takes over the screen. A rejection is
// returned as is; a rendezvous that cannot be reached comes back wrapped in
// ErrRetrying, and is retried as a drop would be. From then on it holds the
// registration open in the background, reconnecting on drops, until ctx is
// cancelled. Later failures go to logf: printing mid-session would scribble
// over whatever is on screen.
//
// Liveness is the connection itself. When this process exits for any reason,
// including SIGKILL, the kernel closes the socket, the rendezvous drops the
// entry, and every viewer's channel closes with it.
func Register(ctx context.Context, t Target, logf func(string, ...any)) error {
	c, err := open(ctx, t, logf)
	if err != nil && !unreachable(err) {
		return err
	}
	go keep(ctx, t, c, logf)
	if err != nil {
		return fmt.Errorf("%w (%w)", err, ErrRetrying)
	}
	return nil
}

// unreachable is a failure the network caused, which waiting can fix; a
// refused key or a rejected registration is not.
func unreachable(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) || errors.Is(err, io.EOF)
}

// keep holds c open, reconnecting when it drops. A nil c is one that never
// connected.
func keep(ctx context.Context, t Target, c *ssh.Client, logf func(string, ...any)) {
	backoff := time.Second
	for {
		if c != nil {
			err := c.Wait()
			if ctx.Err() != nil {
				return
			}
			logf("registration dropped: %v", err)
		}
		for {
			var err error
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, maxBackoff)
			if c, err = open(ctx, t, logf); err == nil {
				logf("re-registered")
				backoff = time.Second
				break
			}
			logf("re-registration failed: %v", err)
		}
	}
}

// open connects, starts the register command, and waits for the rendezvous
// to acknowledge. The returned client stays connected until the rendezvous
// goes away or ctx is cancelled.
func open(ctx context.Context, t Target, logf func(string, ...any)) (*ssh.Client, error) {
	d := net.Dialer{Timeout: handshakeTimeout}
	conn, err := d.DialContext(ctx, "tcp", t.Addr)
	if err != nil {
		return nil, fmt.Errorf("connecting to rendezvous %s: %w", t.Addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))

	sc, chans, reqs, err := ssh.NewClientConn(conn, t.Addr, t.Config)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("authenticating to rendezvous %s: %w", t.Addr, err)
	}
	client := ssh.NewClient(sc, chans, reqs)

	// Viewers arrive as channels the rendezvous opens on this connection.
	// Listen before registering, since the first can arrive right after the
	// acknowledgement.
	if t.OnViewer != nil {
		viewers := client.HandleChannelOpen(tunnel.ChannelType)
		go func() {
			for nc := range viewers {
				t.OnViewer(nc)
			}
		}()
	}

	sess, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("opening registration channel: %w", err)
	}
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	stdout, err := sess.StdoutPipe()
	if err != nil {
		client.Close()
		return nil, err
	}
	// Never written to. The rendezvous blocks reading it, and the entry lives
	// until it sees EOF.
	if _, err := sess.StdinPipe(); err != nil {
		client.Close()
		return nil, err
	}
	if err := sess.Start(t.Command); err != nil {
		client.Close()
		return nil, fmt.Errorf("starting registration: %w", err)
	}

	ack, _ := bufio.NewReader(stdout).ReadString('\n')
	if !strings.HasPrefix(ack, "registered ") {
		_ = sess.Wait()
		client.Close()
		return nil, rejection(stderr.String())
	}
	_ = conn.SetDeadline(time.Time{})
	// "registered NAME (ID)": the id is new with every registration, so the
	// log says which one the lines that follow belong to.
	logf("%s", strings.TrimSpace(ack))

	go func() {
		t := time.NewTicker(keepAlive)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				client.Close()
				return
			case <-t.C:
				if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
					client.Close()
					return
				}
			}
		}
	}()
	return client, nil
}

// rejection turns what the rendezvous said on stderr into the error to show.
// A rendezvous that does not know the share command is one from before the
// tunnel moved onto this connection.
func rejection(stderr string) error {
	msg := strings.TrimSpace(stderr)
	if strings.Contains(msg, "unsupported command: share") {
		return errors.New("the rendezvous is older than this ush: redeploy it")
	}
	if msg == "" {
		msg = "no acknowledgement"
	}
	return fmt.Errorf("rendezvous rejected registration: %s", msg)
}
