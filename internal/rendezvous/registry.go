package rendezvous

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// The registry is what `ush -s` puts here and the menu reads back. It used to
// be written by a shell script through jq, and both halves of that are part of
// the contract: what is accepted, and how it is spelled.
//
// Everything here is remote input from a sharer, and it ends up rendered on
// someone else's terminal, so free text is cut down to printable ASCII. The
// shell did that with `${v//[^[:print:]]/}` in the C locale, where isprint is
// 0x20-0x7e, and truncated by bytes; widening that to unicode.IsPrint would
// let combining marks, double-width characters and homoglyphs through.
var (
	validSessionID = regexp.MustCompile(`^[A-Za-z0-9]{20}$`)
	validName      = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)
	validUser      = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)
)

// Field lengths, in bytes, as the shell script capped them.
const (
	maxMachine     = 32
	maxDescription = 80
	maxCommand     = 200
	maxCwd         = 200
)

// sessionIDAlphabet is what validSessionID accepts.
const sessionIDAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// newSessionID makes the id a share is known by: 20 random alphanumerics,
// assigned here rather than by the sharer so no two shares can collide and
// no sharer can pose as another.
func newSessionID() (string, error) {
	b := make([]byte, 20)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(sessionIDAlphabet))))
		if err != nil {
			return "", err
		}
		b[i] = sessionIDAlphabet[n.Int64()]
	}
	return string(b), nil
}

// clean keeps printable ASCII and truncates to n bytes.
func clean(s string, n int) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x20 && s[i] <= 0x7e {
			b.WriteByte(s[i])
		}
	}
	out := b.String()
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// parseRegistration decodes one `share` payload: base64 of key=value
// lines. Unknown keys are dropped, a repeated key keeps its last value, and
// lines without a '=' are ignored -- the same shape the shell's read loop had.
func parseRegistration(blob string, now time.Time) (session, error) {
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return session{}, fmt.Errorf("share: payload is not base64")
	}

	fields := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "name", "description", "machine", "command", "cwd", "user", "read_only":
			fields[key] = value
		}
	}

	s := session{
		Name:         fields["name"],
		Machine:      clean(fields["machine"], maxMachine),
		User:         fields["user"],
		Command:      clean(fields["command"], maxCommand),
		Cwd:          clean(fields["cwd"], maxCwd),
		Description:  clean(fields["description"], maxDescription),
		ReadOnly:     fields["read_only"] == "true",
		Started:      now.UTC().Format("2006-01-02T15:04:05Z"),
		StartedEpoch: now.Unix(),
	}
	if !validName.MatchString(s.Name) {
		return session{}, fmt.Errorf("share: bad name")
	}
	if s.Machine == "" {
		s.Machine = "unknown"
	}
	if !validUser.MatchString(s.User) {
		s.User = ""
	}
	return s, nil
}

// writeEntry stores one registration. The write is atomic so the menu, which
// reads this directory every couple of seconds, never sees half a file.
func writeEntry(dir string, s session) error {
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	final := filepath.Join(dir, s.ID+".json")
	tmp, err := os.CreateTemp(dir, s.ID+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), final)
}

func removeEntry(dir, id string) {
	_ = os.Remove(filepath.Join(dir, id+".json"))
}

// listJSON is the `list` command's output. Its exact spelling is part of the
// contract -- two-space indent, a space after each colon, the field order of
// the session struct -- because clients grep it, so it matches what `jq -s`
// produced for the shell script, down to the bare [] for an empty registry.
// A session someone is watching gains a trailing "viewers" count.
func listJSON(dir, viewersDir string) []byte {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	out := []session{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var s session
		if json.Unmarshal(b, &s) != nil || s.ID == "" {
			continue
		}
		s.Viewers = countViewers(viewersDir, s.ID)
		out = append(out, s)
	}
	if len(out) == 0 {
		return []byte("[]\n")
	}
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return []byte("[]\n")
	}
	return append(body, '\n')
}
