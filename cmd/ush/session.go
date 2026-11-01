package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"ush/internal/keep"
	"ush/internal/register"
)

const sessionUsage = "usage: ush session set [-n NAME] [-d DESCRIPTION] [SESSION]\n" +
	"  changes what the menu calls a session shared from this machine; SESSION\n" +
	"  defaults to $USH_SESSION, the one this shell is in. -d '' clears the\n" +
	"  description. The new name is the menu's: ush attach and ush stop still\n" +
	"  take the one the session started with.\n"

// runSession is `ush session set`: relabel a share from the machine sharing
// it, by asking the process that holds the share. That process keeps the
// label and sends it to the rendezvous, now if it can and at every
// registration after, so an edit made while the rendezvous is away still
// lands.
func runSession(args []string, getenv func(string) string, out io.Writer) int {
	if len(args) == 0 || args[0] != "set" {
		fmt.Fprint(os.Stderr, sessionUsage)
		return 2
	}
	fs := flag.NewFlagSet("ush session set", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("n", "", "")
	desc := fs.String("d", "", "")
	if err := fs.Parse(args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "ush: %v\n%s", err, sessionUsage)
		return 2
	}
	var setName, setDesc *string
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "n":
			setName = name
		case "d":
			setDesc = desc
		}
	})
	session := getenv("USH_SESSION")
	switch {
	case fs.NArg() == 1:
		session = fs.Arg(0)
	case fs.NArg() > 1:
		fmt.Fprintf(os.Stderr, "ush: one session at a time (flags go before its name)\n%s", sessionUsage)
		return 2
	}
	if setName == nil && setDesc == nil {
		fmt.Fprintf(os.Stderr, "ush: nothing to set: give -n, -d or both\n%s", sessionUsage)
		return 2
	}
	if session == "" {
		fmt.Fprintf(os.Stderr, "ush: which session? Run this inside one, or name it\n%s", sessionUsage)
		return 2
	}
	if !validName.MatchString(session) {
		fmt.Fprintf(os.Stderr, "ush: no session can be called %q\n", session)
		return 2
	}
	dir, err := keep.Dir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ush: %v\n", err)
		return 1
	}
	newName, newDesc, err := register.SetLabel(register.ControlPath(dir, session), setName, setDesc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ush: session %q: %v\n", session, err)
		return 1
	}
	// The share has it; the menu does once the rendezvous does, which is now
	// unless it is away.
	if newDesc == "" {
		fmt.Fprintf(out, "%s will be listed as %s\n", session, newName)
	} else {
		fmt.Fprintf(out, "%s will be listed as %s: %s\n", session, newName, newDesc)
	}
	return 0
}

// controlled makes the label a share is listed under, and answers for it on
// a control socket named after the share, for ush session set and the Ctrl-]
// screen. Whoever shares in a reconnect loop makes it once, outside the loop,
// so that it lasts as long as the share. Without the socket the share still
// runs; it just cannot be relabelled from this machine.
func controlled(name, desc string, logf func(string, ...any)) (*register.Label, func()) {
	label := register.NewLabel(name, desc)
	dir, err := keep.Dir()
	if err != nil {
		logf("cannot be relabelled from here: %v", err)
		return label, func() {}
	}
	ln, err := register.ServeControl(register.ControlPath(dir, name), label)
	if err != nil {
		logf("cannot be relabelled from here: %v", err)
		return label, func() {}
	}
	return label, func() { _ = ln.Close() }
}
