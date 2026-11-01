package register

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ValidName is what a session may be called: what the rendezvous accepts,
// less a leading punctuation mark.
var ValidName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

// MaxDescription is the rendezvous's cap on a description, in bytes.
const MaxDescription = 80

// Label is what the menu calls a session: its name and description. It is
// the part of a share that changes while it runs, from either end: the
// sharer (ush session set, the Ctrl-] screen) or the rendezvous (the menu's
// panel, the iOS app). Every registration sends it as it is then, so an edit
// outlives reconnects, and one made while the rendezvous is away still lands.
type Label struct {
	mu          sync.Mutex
	name        string
	description string
	// newName and newDescription mark fields edited here and not yet sent.
	// Only those go to the rendezvous, so an edit here never carries back a
	// stale copy of a field someone changed from the menu meanwhile.
	newName, newDescription bool
	// unsent is signalled by a local edit, for the connection that is up
	// (or the next one) to pass on.
	unsent chan struct{}
}

func NewLabel(name, description string) *Label {
	return &Label{name: name, description: description, unsent: make(chan struct{}, 1)}
}

func (l *Label) Get() (name, description string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.name, l.description
}

// Set edits the label here and has it sent to the rendezvous. A nil field is
// left as it is.
func (l *Label) Set(name, description *string) error {
	if name != nil && !ValidName.MatchString(*name) {
		return errors.New("a name is 1-32 of A-Z a-z 0-9 . _ -, starting with a letter or digit")
	}
	if description != nil {
		if err := checkDescription(*description); err != nil {
			return err
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if name != nil {
		l.name, l.newName = *name, true
	}
	if description != nil {
		l.description, l.newDescription = *description, true
	}
	select {
	case l.unsent <- struct{}{}:
	default: // one is already waiting, and it sends the label as it is then
	}
	return nil
}

// registering is the label for a new registration, which carries all of it,
// so nothing edited here is left to send after.
func (l *Label) registering() (name, description string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.newName, l.newDescription = false, false
	return l.name, l.description
}

// edits is the update line for what was edited here since the last one, and
// counts it as sent. It is "" when nothing was.
func (l *Label) edits() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var b strings.Builder
	if l.newName {
		b.WriteString("name=" + oneLine(l.name) + "\n")
	}
	if l.newDescription {
		b.WriteString("description=" + oneLine(l.description) + "\n")
	}
	l.newName, l.newDescription = false, false
	if b.Len() == 0 {
		return ""
	}
	return "update " + base64.StdEncoding.EncodeToString([]byte(b.String())) + "\n"
}

// checkDescription refuses what the rendezvous would quietly cut down, so the
// menu shows what was typed.
func checkDescription(s string) error {
	if len(s) > MaxDescription {
		return fmt.Errorf("a description is at most %d characters", MaxDescription)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return errors.New("a description is plain ASCII: no accents, emoji or control characters")
		}
	}
	return nil
}

// apply folds in an "update BASE64" line from the rendezvous: the label as
// its entry now has it. Unlike Set, it is not sent back. A field edited here
// and not yet sent keeps its new value: the rendezvous answers that edit with
// the label as saved, and that answer is the one to keep. It reports whether
// the label changed.
func (l *Label) apply(line string) (name, description string, changed bool) {
	name, description, ok := parseUpdate(line)
	if !ok {
		return "", "", false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.newName {
		name = l.name
	}
	if l.newDescription {
		description = l.description
	}
	if l.name == name && l.description == description {
		return name, description, false
	}
	l.name, l.description = name, description
	return name, description, true
}

// updateLine is a whole label as it travels on a share's connection, and
// from the control socket: "update " and base64 of key=value lines, the
// share payload's format.
func updateLine(name, description string) string {
	return "update " + base64.StdEncoding.EncodeToString(
		[]byte("name="+oneLine(name)+"\ndescription="+oneLine(description)+"\n")) + "\n"
}

func parseUpdate(line string) (name, description string, ok bool) {
	blob, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "update ")
	if !ok {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return "", "", false
	}
	for _, l := range strings.Split(string(raw), "\n") {
		key, value, _ := strings.Cut(l, "=")
		switch key {
		case "name":
			name = value
		case "description":
			description = value
		}
	}
	return name, description, name != ""
}

// ControlPath is where the process holding a share listens for edits to its
// label: in dir, under the name the share started with, which is the
// $USH_SESSION its shells have.
func ControlPath(dir, name string) string {
	return filepath.Join(dir, name+".ctl")
}

// ServeControl answers on path for l, until the returned listener is closed:
// "get", or "set BASE64" with the fields to change, one request per
// connection. Either is answered with "ok BASE64" and the label as it then
// is, or "error MESSAGE". A socket another share still answers on is left
// alone, and that is an error.
func ServeControl(path string, l *Label) (io.Closer, error) {
	if c, err := net.Dial("unix", path); err == nil {
		c.Close()
		return nil, fmt.Errorf("%s is already in use by another share", path)
	} else if errors.Is(err, syscall.ECONNREFUSED) {
		_ = os.Remove(path) // left behind by a share that is gone
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go answer(c, l)
		}
	}()
	return ln, nil
}

func answer(c net.Conn, l *Label) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	req, err := bufio.NewReader(io.LimitReader(c, 4096)).ReadString('\n')
	if err != nil {
		return
	}
	req = strings.TrimSuffix(req, "\n")
	if req != "get" {
		blob, ok := strings.CutPrefix(req, "set ")
		raw, err := base64.StdEncoding.DecodeString(blob)
		if !ok || err != nil {
			fmt.Fprintf(c, "error bad request\n")
			return
		}
		var name, description *string
		for _, line := range strings.Split(string(raw), "\n") {
			key, value, _ := strings.Cut(line, "=")
			switch key {
			case "name":
				name = &value
			case "description":
				description = &value
			}
		}
		if err := l.Set(name, description); err != nil {
			fmt.Fprintf(c, "error %s\n", err)
			return
		}
	}
	name, description := l.Get()
	fmt.Fprintf(c, "ok %s", strings.TrimPrefix(updateLine(name, description), "update "))
}

// GetLabel asks the share listening on path for its label.
func GetLabel(path string) (name, description string, err error) {
	return control(path, "get")
}

// SetLabel has the share listening on path change its label; a nil field is
// left as it is. It returns the label as it then is.
func SetLabel(path string, name, description *string) (string, string, error) {
	var b strings.Builder
	if name != nil {
		b.WriteString("name=" + oneLine(*name) + "\n")
	}
	if description != nil {
		b.WriteString("description=" + oneLine(*description) + "\n")
	}
	return control(path, "set "+base64.StdEncoding.EncodeToString([]byte(b.String())))
}

func control(path, req string) (string, string, error) {
	c, err := net.DialTimeout("unix", path, 5*time.Second)
	if err != nil {
		return "", "", errors.New("no share is listening for it here")
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, req+"\n"); err != nil {
		return "", "", err
	}
	reply, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return "", "", fmt.Errorf("the share did not answer: %v", err)
	}
	if msg, ok := strings.CutPrefix(reply, "error "); ok {
		return "", "", errors.New(strings.TrimSpace(msg))
	}
	rest, ok := strings.CutPrefix(reply, "ok ")
	if !ok {
		return "", "", errors.New("the share's answer made no sense")
	}
	name, description, ok := parseUpdate("update " + rest)
	if !ok {
		return "", "", errors.New("the share's answer made no sense")
	}
	return name, description, nil
}
