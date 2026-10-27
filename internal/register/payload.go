// Package register announces a shared session to the rendezvous and holds the connection every viewer rides.
package register

import (
	"encoding/base64"
	"strconv"
	"strings"
)

// Info is what the menu shows about a session. It is a snapshot taken when the
// session starts.
type Info struct {
	Name        string
	Description string
	Machine     string
	Command     string
	Cwd         string
	User        string
	ReadOnly    bool
}

// Command is the remote command sent to the rendezvous: share <blob>.
//
// The payload travels as a single base64 argument of key=value lines. One
// argument means there is nothing for SSH_ORIGINAL_COMMAND word-splitting to
// drop or shift, and empty values survive as "key=". The rendezvous whitelists
// keys and sanitises values, so this format does not need to be defensive
// beyond keeping one field per line.
func Command(i Info) string {
	fields := []struct{ key, value string }{
		{"name", i.Name},
		{"description", i.Description},
		{"machine", i.Machine},
		{"command", i.Command},
		{"cwd", i.Cwd},
		{"user", i.User},
		{"read_only", strconv.FormatBool(i.ReadOnly)},
	}
	var b strings.Builder
	for _, f := range fields {
		b.WriteString(f.key)
		b.WriteByte('=')
		b.WriteString(oneLine(f.value))
		b.WriteByte('\n')
	}
	return "share " + base64.StdEncoding.EncodeToString([]byte(b.String()))
}

// oneLine keeps a value from spilling into, and forging, the next field.
func oneLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}
