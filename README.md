# ush

Named, browsable terminal sharing through a rendezvous you run.

A machine behind NAT shares a terminal by holding one SSH connection open
to the rendezvous; viewers pick it from a menu by name, and the rendezvous
carries the terminal between them. Session IDs exist but no human types one.

```
laptop$  ush share -n api -- claude                 # share, under a name

ipad$    ssh ush@rendezvous                         # always the same endpoint
laptop$  ush                                        # same thing, using ~/.ushrc
         ┌─────────────────────────────────────┐
         │ NAME  MACHINE COMMAND          AGE  │
         │ api   laptop  claude           2m   │   ← type to filter; enter joins
         │ build ci-pod  make watch       14m  │
         └─────────────────────────────────────┘
```

The client's connection details never change: same host, same user, same key.
Session IDs stay on the rendezvous and are never typed by a human.

## How it works

| Piece | Role |
|:--|:--|
| `ushd` | the rendezvous: one port, its own SSH server, the registry, the menu, and the tunnel. No sshd, no forced command, no relay beside it |
| `ushd _session` | one session on a real pty: `ushd` re-executed. The menu (Charm's bubbletea) with a live list, a detail card, and in-process joins with a `Ctrl-]` detach key that works mid-line |
| `cmd/ush` | the sharer, in Go: runs the command on a pty, registers it, and serves viewers the rendezvous opens on that same connection |

`ush` holds one SSH connection open to the rendezvous for the life of the
session. That connection **is** the registration and **is** the tunnel: the
rendezvous opens a channel back on it for every viewer. When `ush` exits for
any reason, SIGKILL included, the kernel closes the socket, the entry
disappears, and every viewer's channel closes with it. No heartbeats, no
TTLs, no reaping.

## Ports

The rendezvous has **one**: 7874 (`port` in `~/.ushrc`). Sharers, viewers,
the iPad app and plain OpenSSH all speak to `ushd` on it. `ush ping` checks
it.

## Try it locally

```sh
make seed                                     # .env with USH_SEED (the shared key)
go test ./...                                 # unit tests
./test/e2e.sh                                 # builds everything and proves it works

docker compose up -d                          # menu tests need it running
uv run --no-project python -m unittest test/menu_test.py
```

The menu tests drive the menu through real PTYs; they are test-only Python
and nothing ships with it.

`test/e2e.sh` shares a session from your machine through the containerised
rendezvous, finds it in the registry, joins it, types into it, and checks the
entry vanishes when the session ends — including on SIGKILL.

To poke at it by hand:

```sh
make seed                               # once: writes .env (gitignored)
docker compose up -d --build            # the rendezvous derives its key from it

make install                            # ush onto your PATH
ush init --seed --rendezvous 127.0.0.1 --port 7874 \
         --skip-host-key-check          # paste the USH_SEED value from .env

ush share -n demo                       # terminal 1: shares $SHELL
ush                                     # terminal 2: the menu
```

`ush init` prints the key's fingerprint and the rendezvous logs its own
(`docker compose logs rendezvous`); they match when the seeds do.

## Usage

| Command | Does |
|:--|:--|
| `ush` | open the menu; pick a session to join it |
| `ush connect api` | join "api" directly, skipping the menu (`ush connect` alone is the menu) |
| `ush connect api -m nas` | the "api" shared from machine nas, when more than one machine has one. Twins on one machine are listed by id: `ush connect ID` |
| `ush connect --list` | print the live sessions, with their machines and ids (`-m nas` for one machine's) |
| `ush share` | share `$SHELL`, named after the host (`ush serve` and `ush -s` are the same). A rendezvous that can't be reached doesn't stop it: the shell starts, and is listed once it can |
| `ush share -n api -d "staging" -- claude` | share `claude` as "api", run as a job of your shell: `^Z` drops to the shell, `fg` brings it back |
| `ush share --no-shell -- claude` | run it on its own instead, with no shell around it |
| `ush share -k -n api` | keep your shell running: it **survives this window closing and rendezvous restarts**, stays listed, and `ush attach api` gets back to it |
| `ush share -k -n api -- claude` | the same for a command; the session ends when the command does |
| `ush share -k --mux tmux -n api` | kept by tmux (or herdr) instead of ush itself; see below |
| `ush adopt 4242 -n api` | move a process already running elsewhere into a share in this window. macOS only, and not for Apple's own binaries or hardened apps like `claude`: lldb is refused. Its old window still owns `^C`, `^Z` and hangup, and its children stay there |
| `ush attach api` / `ush stop api` | get back to a session ush keeps, or end it |
| `ush --profile work` | use another profile from `~/.ushrc` |
| `ush init` | add a profile to `~/.ushrc` (creating it), generating the key if missing |
| `ush init --seed` | same, deriving the key from the shared seed (paste the `USH_SEED` value) |
| `ush seed` | print a new random seed |
| `ush ping` | check that the rendezvous can be reached and logged in to |
| `ush pair` | show this profile as a QR code for the iOS app (it carries the key: anyone who sees it can join everything) |
| `Ctrl-]` inside a joined session | open the session panel: viewers, time on, uptime; `b` back to session, `r` refresh the screen, `l` back to list, `n` rename, `d` or `q` disconnect |

What keeps a `-k` session is `--mux`, or `mux = "tmux"` in `~/.ushrc` (top
level, overridable per profile):

| `--mux` | Keeps | With the window closed | Get back to it |
|:--|:--|:--|:--|
| `ush` (default) | the command and the share | still listed and joinable | `ush attach NAME` |
| `tmux` | the command | unlisted until `ush share -k` runs for it again | `tmux attach -t NAME`, or `ush share -k` again |
| `herdr` | a shell only: herdr sessions outlive their command | as tmux | `herdr --session NAME`, or `ush share -k` again |

With tmux or herdr each viewer gets a client of their own, sized to their
screen. tmux and herdr names can't contain `.` or `:`, and herdr refuses a
zero-sized terminal, so `--mux herdr` needs a real terminal, not a script or
cron job.

With `-k`, a rendezvous restart or network drop never ends the *attach*: the
registration reconnects on its own in the background (backoff 1s → 30s),
logged rather than printed, so nothing interrupts your screen. It reappears
under the same name with a new session id, so viewers pick it from the list
again. The attach ends when the command exits or when you detach (`Ctrl-]`,
which asks first with `--mux ush`: `d` leaves, any other key goes back; or
tmux's own detach key); the session keeps running after a detach.

A shared command runs as the first job of your `$SHELL` (zsh or bash), after
your rc files, in the directory you shared from, and the share ends with the
command's own exit status. So `^Z` drops to that shell and `fg` brings the
command back, as in any terminal, and the command can be an alias or a
function from your rc files. The share ends when the command does, whether
or not it was suspended on the way: quit it after an `fg` and the shell goes
with it. Run on its own, a command has nobody above
it to resume it: the kernel ignores `^Z` for it, and a program that has
already cleared its screen to suspend (Claude Code does) waits forever.
`--no-shell` runs it on its own anyway; another `$SHELL`, or a command that is
itself a shell, runs as it is.

Inside a shared session, `USH_SESSION` is set to the session's name, so a
prompt or script can tell it is being shared. With
[starship](https://starship.rs), this shows `ush:api` in the prompt of a
shared shell and nothing anywhere else:

```toml
# ~/.config/starship.toml
[env_var.USH_SESSION]
format = "[ush:$env_value](bold yellow) "
```

The default prompt picks it up as it is; with a `format` of your own, put
`${env_var.USH_SESSION}` where it should appear.

Connecting is the default; sharing is `ush share` (or `ush serve`, `ush -s`),
first on the line. Share options without it are an error rather than silently
ignored.

`ush init` asks for the rendezvous host, ports, key path and profile name,
showing defaults; flags skip the questions and `-y` takes the defaults
(`ush init -y --rendezvous rv.example.net`). It appends to an existing file
without touching the rest of it, refuses a duplicate profile name, and never
overwrites a key.

Your machine shows in the menu's MACHINE column under its short hostname. To
use a friendlier name, set `machine` at the top of `~/.ushrc`; a profile can
override it, and empty or unset falls back to the hostname. It is also the
default session name, with anything but letters, digits, `_` and `-` turned
into dashes:

```toml
machine = "Alex's MacBook"
default = "home"

[profiles.work]
rendezvous = "rv.work.example"
machine = "alex-work"      # only on this rendezvous
```

`~/.ushrc` is TOML. Each `[profiles.NAME]` table needs only `rendezvous`; the
rest default to `port = 7874`, `user = "ush"`,
`key = "~/.ssh/ush_ed25519"`,
`known_hosts = "~/.ssh/known_hosts"`. Profile selection: `--profile`, then
`$USH_PROFILE`, then the top-level `default`. Unknown keys are an error, so a
typo cannot silently fall back to a default.

## Examples

**Share, and watch from somewhere else**

```sh
ush share -n api                            # your shell, listed as "api"
ush share -n logs -- tail -f /var/log/app.log
ush share -n demo --read-only               # viewers watch; their keys are dropped

ush                                         # another machine: the menu, enter joins
ssh -p 7874 ush@rendezvous                  # the same menu from plain OpenSSH
```

Inside a joined session, `Ctrl-]` opens the panel; `d` or `q` disconnects.

**A different rendezvous.** `share` goes first, so `--profile` goes after it
there; or set `USH_PROFILE` once:

```sh
ush share --profile work -n api
ush --profile work
USH_PROFILE=work ush share -n api
```

**Keep it running** after the window closes:

```sh
ush share -k -n work                        # your shell, kept by ush; still listed with the window closed
ush share -k -n dev -- npm run dev          # a command; the session ends when it exits
ush attach --list                           # what ush is keeping
ush attach work                             # back into it (Ctrl-] then d detaches again)
ush stop work                               # end it
```

**A one-word share** for bash: a function in `~/.bashrc` that keeps a
session named after the directory you are in:

```sh
us() { ush share -n "${PWD##*/}" -k -- "${@:-bash}"; }

cd ~/src/api
us                                          # a kept bash, listed as "api"
us claude                                   # claude instead, under the same name
```

**tmux**

```sh
ush share -k --mux tmux -n work             # a new tmux session "work"; each viewer gets its own client
ush share -k --mux tmux -n dev -- npm run dev
ush share -n main -- tmux attach -t main    # a tmux session you already have
```

With `--mux tmux` the command outlives the window, but the share lives in the
window's `ush`: closing it unlists the session until `ush share -k --mux tmux -n
work` runs again, which reattaches. Sharing an existing tmux session puts every
viewer on one client, sized to the smallest screen; run that one from outside
tmux, or with `TMUX=` in front, since tmux refuses to nest.

**herdr**

```sh
herdr session list                          # the named sessions
herdr --session work                        # attach to "work", creating it if missing
herdr session attach work                   # attach to an existing "work" from another terminal
ush share -k --mux herdr -n work            # attach to herdr session "work" (creating it) and share it
```

Sharing it works like tmux: each viewer gets a herdr client of their own, the
herdr session outlives the window, and closing that window unlists the share
until `ush share -k --mux herdr -n work` runs again, which reattaches. With
`mux = "herdr"` in `~/.ushrc`, `ush share -k -n work` does the same. herdr
keeps a shell only, and needs a real terminal (not a script or cron job).

**Run a rendezvous on this machine**, no container:

```sh
make seed build bin/ushd                    # .env with USH_SEED, and both binaries
USH_SEED=$(sed -n 's/^USH_SEED=//p' .env) ./bin/ushd -listen :7874 -run-dir ~/.local/ushd

# another terminal: a profile for it, with the key derived from the same seed
USH_SEED=$(sed -n 's/^USH_SEED=//p' .env) ./bin/ush init -y --seed --profile local \
    --rendezvous 127.0.0.1 --port 7874 --key "$PWD/.local/local-psk"
./bin/ush --profile local ping              # rendezvous  127.0.0.1:7874  ok, ...
```

`ushd` needs only the seed, a writable run directory and the port. Use
`-listen 127.0.0.1:7874` to keep it off the network; other machines and the
iPad reach `:7874` at this machine's address.

## iPhone and iPad

`ios/` is a native app that replaces `ssh ush@rendezvous` from an iPad or
phone: a tree of rendezvous and their sessions, several sessions open as tabs,
activity and bell marks. Set it up by scanning `ush pair`. See
[ios/README.md](ios/README.md).

## Deploying a rendezvous

`rendezvous/Dockerfile` builds the image and `docker-compose.yml` runs it:
`ushd` needs only `USH_SEED`, a writable run directory and its one port. Run
one instance. The registry and every sharer's tunnel live in that process, so
**every restart ends all live sessions**. Clients point at it with
`ush init --seed --rendezvous HOST --port 7874`.

**Upgrading from a ush older than 2026-09-26** (the upterm era): deploy the
rendezvous and reinstall `ush` together. An old `ush share` against the new
rendezvous fails to log in ("your key was rejected": its tunnel used a
different username); a new `ush` against an old rendezvous says the
rendezvous is older than this ush.

## Credits

Big thanks to [Owen Ou](https://github.com/owenthereal) for
[upterm](https://github.com/owenthereal/upterm). ush started out as a menu
bolted onto upterm, and the basic idea of sharing a terminal by dialing out
to an SSH server that viewers connect through came from there. ush ran on
upterm until September 2026.

Some of upterm's code still lives here: `internal/termio` is its `io`
package (Apache-2.0). It handles sending output to every viewer, dropping
viewers who fall behind, and keeping terminal queries away from viewers.

## Security

One pre-shared ed25519 key does everything: hosts authenticate to the
rendezvous to share, the rendezvous's session child authenticates back to it
over a unix socket, and you authenticate to the rendezvous to browse. A
sharer never sees a viewer's key: it trusts the rendezvous it authenticated
to and whose host key it verified, so whoever holds the key can view
everything, and the rendezvous sees session plaintext. That key lists and joins
**everything** — losing it means rotating it on every host. This is the
deliberate solo-use trade-off; multi-user identity is out of scope.

The key comes from a **seed**: 32 random bytes written as 52 base32
characters. An ed25519 private key *is* a 32-byte seed, so the same seed gives
the same key on every machine, with its full 256 bits and no KDF. The seed is
the key in another form: treat `.env` like a private key. The rendezvous reads
it as `USH_SEED` at startup (or `-seed-file`), derives every key, then zeroes
it and unsets the variable; it does remain in the container's configuration, visible
to anyone with Docker (or, in k8s, Secret) access. A generic SSH app on an iPad cannot derive keys, so it imports the key file
`ush init --seed` writes; the ush iOS app derives them itself from the seed in
`ush pair`'s QR code.

The rendezvous's **host key** comes from the seed too (HKDF), so it survives
rebuilds and clients' `known_hosts` entries stay valid. `ushd` offers only
that ed25519 key, and logs its fingerprint alongside the shared key's at
startup.

There is no shell on the rendezvous to get: `ushd` serves four commands
(`share`, `list`, `join`, the menu) and refuses everything else, with
forwarding and subsystems refused as well; an old client's `register` is
refused too, with a message to upgrade. A viewer can also open the
`viewer@ush.acheris.net` channel type directly on the public port, bypassing
the menu, at the same trust level. Every session runs as an unprivileged
user, in a child process of its own.

## Status

Works end to end. Both halves are Go: the `ush` client, and `ushd`, the whole
rendezvous in one binary.

`ushd` runs as well on a Mac as in the cluster -- `test/with-ushd.sh` starts it
on loopback, which is how the menu suite runs without Docker.

## License

ush is licensed under the [Apache License 2.0](LICENSE). The code in
`internal/termio` comes from upterm and keeps its own copy of the same
license.
