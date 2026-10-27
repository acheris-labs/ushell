package config

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

var (
	validProfileName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	tableHeader      = regexp.MustCompile(`^\s*\[`)
	defaultLine      = regexp.MustCompile(`^\s*default\s*=`)
)

const header = `# ush config. See https://github.com/acheris-labs/ushell
# Pick a profile with --profile NAME or $USH_PROFILE; otherwise default is used.
`

// Inspect reports the default profile and the profile names in a config's
// text. Empty text is a valid, empty config.
func Inspect(text string) (def string, names []string, err error) {
	var f file
	if _, err := toml.Decode(text, &f); err != nil {
		return "", nil, err
	}
	for n := range f.Profiles {
		names = append(names, n)
	}
	return f.Default, names, nil
}

// AddProfile returns existing with a [profiles.NAME] table appended. The rest
// of the text, comments included, is left as it was. The new profile becomes
// the default if the file has none, or if makeDefault is set.
func AddProfile(existing, name string, p Profile, makeDefault bool) (string, error) {
	if !validProfileName.MatchString(name) {
		return "", fmt.Errorf("invalid profile name %q: use letters, digits, - and _", name)
	}
	def, names, err := Inspect(existing)
	if err != nil {
		return "", fmt.Errorf("existing config does not parse: %w", err)
	}
	for _, n := range names {
		if n == name {
			return "", fmt.Errorf("profile %q already exists", name)
		}
	}

	var block bytes.Buffer
	fmt.Fprintf(&block, "[profiles.%s]\n", name)
	if err := toml.NewEncoder(&block).Encode(p); err != nil {
		return "", err
	}

	text := existing
	if strings.TrimSpace(text) == "" {
		text = header
	}
	switch {
	case def == "":
		text = insertTopLevel(text, fmt.Sprintf("default = %q\n", name))
	case makeDefault:
		text = replaceTopLevelDefault(text, name)
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text + "\n" + block.String(), nil
}

// insertTopLevel puts line above the first table header. A top-level key
// written after a header would belong to that table instead.
func insertTopLevel(text, line string) string {
	lines := strings.SplitAfter(text, "\n")
	for i, l := range lines {
		if tableHeader.MatchString(l) {
			return strings.Join(lines[:i], "") + line + "\n" + strings.Join(lines[i:], "")
		}
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text + line
}

func replaceTopLevelDefault(text, name string) string {
	lines := strings.SplitAfter(text, "\n")
	for i, l := range lines {
		if tableHeader.MatchString(l) {
			break
		}
		if defaultLine.MatchString(l) {
			lines[i] = fmt.Sprintf("default = %q\n", name)
			break
		}
	}
	return strings.Join(lines, "")
}
