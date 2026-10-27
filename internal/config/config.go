// Package config reads ~/.ushrc, a TOML file of named profiles:
//
//	default = "local"
//
//	[profiles.local]
//	rendezvous = "127.0.0.1"
//	port = 7874
//	key = "~/src/ut/.local/psk"
//
// Profiles live under [profiles.*] so top-level settings can be added later
// without colliding with a profile name. Unknown keys are an error, so a typo
// cannot silently fall back to a default.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is one resolved profile, with defaults applied and paths expanded.
type Config struct {
	Profile          string
	Machine          string // this machine's name: the setting, else its short hostname
	Mux              string // what keeps a -k session: "ush" (also when empty), "tmux", "herdr"
	Rendezvous       string
	Port             string
	User             string
	Key              string
	KnownHosts       string
	SkipHostKeyCheck bool
}

type file struct {
	Default  string             `toml:"default"`
	Machine  string             `toml:"machine"`
	Mux      string             `toml:"mux"`
	Profiles map[string]Profile `toml:"profiles"`
}

// Profile is one [profiles.NAME] table as written in the file, before
// defaults are applied. Unset fields are omitted when encoding.
type Profile struct {
	Rendezvous       string `toml:"rendezvous"`
	Port             int    `toml:"port,omitempty"` // one port: menu, share and viewer traffic all go over it
	User             string `toml:"user,omitempty"`
	Key              string `toml:"key,omitempty"`
	KnownHosts       string `toml:"known_hosts,omitempty"`
	SkipHostKeyCheck bool   `toml:"skip_host_key_check,omitempty"`
	Machine          string `toml:"machine,omitempty"`
	Mux              string `toml:"mux,omitempty"`
}

// Load reads path and resolves one profile: name if non-empty, otherwise the
// file's default. home is used for defaults and ~ expansion.
func Load(path, home, name string) (Config, error) {
	var f file
	md, err := toml.DecodeFile(path, &f)
	if err != nil {
		return Config{}, fmt.Errorf("reading %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
			if last := k[len(k)-1]; last == "authorized_keys" {
				return Config{}, fmt.Errorf("%s: %s: authorized_keys is gone; the rendezvous admits viewers with the one shared key, so remove it", path, k)
			}
			if last := k[len(k)-1]; last == "ssh_port" || last == "uptermd_port" {
				return Config{}, fmt.Errorf("%s: %s: ssh_port and uptermd_port were replaced by a single port (the rendezvous now serves both on one); use port = 7874", path, k)
			}
		}
		return Config{}, fmt.Errorf("%s: unknown setting %s", path, strings.Join(keys, ", "))
	}

	if name == "" {
		name = f.Default
	}
	if name == "" {
		return Config{}, fmt.Errorf(`%s: no profile selected; set default = "NAME" or pass --profile`, path)
	}
	p, ok := f.Profiles[name]
	if !ok {
		available := make([]string, 0, len(f.Profiles))
		for n := range f.Profiles {
			available = append(available, n)
		}
		slices.Sort(available)
		return Config{}, fmt.Errorf("%s: no profile %q (have: %s)", path, name, strings.Join(available, ", "))
	}
	cfg, err := resolve(path, name, p, home)
	if err != nil {
		return Config{}, err
	}
	// A profile's machine name overrides the top-level one; empty means unset.
	cfg.Machine = or(strings.TrimSpace(p.Machine), or(strings.TrimSpace(f.Machine), shortHostname()))
	cfg.Mux = or(strings.TrimSpace(p.Mux), strings.TrimSpace(f.Mux))
	return cfg, nil
}

func resolve(path, name string, p Profile, home string) (Config, error) {
	where := fmt.Sprintf("%s: profile %q", path, name)
	if p.Rendezvous == "" {
		return Config{}, fmt.Errorf("%s: rendezvous is required", where)
	}
	rvPort, err := port(where, "port", p.Port, 7874)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Profile:          name,
		Rendezvous:       p.Rendezvous,
		Port:             rvPort,
		User:             or(p.User, "ush"),
		Key:              expand(or(p.Key, "~/.ssh/ush_ed25519"), home),
		KnownHosts:       expand(or(p.KnownHosts, "~/.ssh/known_hosts"), home),
		SkipHostKeyCheck: p.SkipHostKeyCheck,
	}
	return cfg, nil
}

func port(where, key string, v, fallback int) (string, error) {
	if v == 0 {
		v = fallback
	}
	if v < 1 || v > 65535 {
		return "", fmt.Errorf("%s: %s %d is not a valid port", where, key, v)
	}
	return strconv.Itoa(v), nil
}

func shortHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	h, _, _ = strings.Cut(h, ".")
	return h
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// ExpandHome expands a leading ~ to home.
func ExpandHome(v, home string) string { return expand(v, home) }

func expand(v, home string) string {
	if v == "~" {
		return home
	}
	if strings.HasPrefix(v, "~/") {
		return filepath.Join(home, v[2:])
	}
	return v
}
