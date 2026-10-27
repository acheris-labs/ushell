package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// A command shared on its own leads its terminal's session with nothing
// above it, and the kernel discards ^Z for such a process group: there is
// nobody to resume it. A program that tears down its screen expecting to be
// stopped (Claude Code does) then waits forever for a SIGCONT. So a shared
// command runs as the first job of the user's own shell, which is what a
// terminal gives it anyway: ^Z drops to that shell, and fg brings it back.

// launch is how to start a command: what to run, and what to add to its
// environment.
type launch struct {
	argv []string
	env  []string
}

// shells are what a command's name is when it already is a shell, and needs
// none around it.
var shells = []string{"sh", "bash", "zsh", "fish", "dash", "ksh", "mksh", "tcsh", "csh"}

// inShell starts command as the first job of shell, which reads the user's rc
// files as usual, runs command in the directory it was started in, and exits
// with its status -- unless it was suspended, in which case the shell stays,
// for fg. home is where ush keeps the rc files that arrange this.
//
// The command runs as it is if it is a shell itself, or shell is one ush does
// not know how to do this for (only zsh and bash).
func inShell(shell string, command []string, home string, getenv func(string) string) (launch, error) {
	as := launch{argv: command}
	if len(command) == 0 || slices.Contains(shells, filepath.Base(command[0])) {
		return as, nil
	}
	run := "USH_RUN=" + commandLine(command)
	dir := filepath.Join(home, ".ush", "shell")
	switch filepath.Base(shell) {
	case "zsh":
		zdot := filepath.Join(dir, "zsh")
		if err := writeIfChanged(filepath.Join(zdot, ".zshenv"), zshenv(zdot)); err != nil {
			return as, err
		}
		if err := writeIfChanged(filepath.Join(zdot, ".zshrc"), zshrc); err != nil {
			return as, err
		}
		env := []string{run, "ZDOTDIR=" + zdot}
		// The user's own ZDOTDIR, if any, is put back before their files are read.
		if z, set := lookup(getenv, "ZDOTDIR"); set {
			env = append(env, "USH_ZDOTDIR="+z)
		}
		return launch{argv: []string{shell, "-i"}, env: env}, nil
	case "bash":
		rc := filepath.Join(dir, "bashrc")
		if err := writeIfChanged(rc, bashrc); err != nil {
			return as, err
		}
		return launch{argv: []string{shell, "--rcfile", rc, "-i"}, env: []string{run}}, nil
	}
	return as, nil
}

// inOwnShell has o's command run as a job of the user's $SHELL, unless
// --no-shell. The menu still shows the command itself.
func inOwnShell(o options) (options, error) {
	l, err := commandLaunch(o)
	if err != nil {
		return o, err
	}
	if o.displayCommand == nil {
		o.displayCommand = o.command
	}
	o.command, o.env = l.argv, l.env
	return o, nil
}

// commandLaunch is how to start o's command: in $SHELL, or bare with
// --no-shell or when there is no home to keep the rc files in.
func commandLaunch(o options) (launch, error) {
	if o.noShell {
		return launch{argv: o.command}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return launch{argv: o.command}, nil
	}
	return inShell(os.Getenv("SHELL"), o.command, home, os.Getenv)
}

// lookup tells an empty variable from a missing one, which getenv cannot.
func lookup(getenv func(string) string, key string) (string, bool) {
	if v := getenv(key); v != "" {
		return v, true
	}
	return "", false
}

// plainWord is a command name a shell reads as one word with nothing to
// expand in it.
var plainWord = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// commandLine writes command as the line the shell runs. A shell expands an
// alias only for a name that is not quoted, so a plain name is left bare, as
// it would be typed at a prompt; anything else, and every argument, is quoted.
func commandLine(command []string) string {
	name, args := command[0], command[1:]
	if !plainWord.MatchString(name) {
		return shellQuote(command)
	}
	if len(args) == 0 {
		return name
	}
	return name + " " + shellQuote(args)
}

// shellQuote writes argv as one line a POSIX shell reads back as the same
// words.
func shellQuote(argv []string) string {
	words := make([]string, len(argv))
	for i, a := range argv {
		words[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(words, " ")
}

func writeIfChanged(path, content string) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, []byte(content)) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Write then rename, so a shell starting meanwhile never reads half a file.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// The rc files. The command arrives as USH_RUN, is taken out of the
// environment before it runs, and runs in the directory the shell started
// in, whatever the user's rc does. A status above 128 that names a stop
// signal means it was suspended: the shell stays, for fg, and from then on
// looks before every prompt for whether the command is still there. Once it
// is gone the shell ends, with the command's own status, as it does when the
// command was never suspended.

// zshenv sends zsh to the user's own files, then back here for .zshrc,
// remembering what their .zshenv left ZDOTDIR as.
func zshenv(here string) string {
	return `# Written by ush (cmd/ush/shell.go): your zsh, then the shared command.
__ush_cwd=$PWD
if (( ${+USH_ZDOTDIR} )); then ZDOTDIR=$USH_ZDOTDIR; else unset ZDOTDIR; fi
unset USH_ZDOTDIR
[[ -r ${ZDOTDIR:-$HOME}/.zshenv ]] && source ${ZDOTDIR:-$HOME}/.zshenv
if (( ${+ZDOTDIR} )); then __ush_zdotdir=$ZDOTDIR; else unset __ush_zdotdir; fi
ZDOTDIR=` + shellQuote([]string{here}) + "\n"
}

// zsh has job control on while reading .zshrc, so the command runs from here.
const zshrc = `# Written by ush (cmd/ush/shell.go): your zsh, then the shared command.
if (( ${+__ush_zdotdir} )); then ZDOTDIR=$__ush_zdotdir; else unset ZDOTDIR; fi
unset __ush_zdotdir
[[ -r ${ZDOTDIR:-$HOME}/.zshrc ]] && source ${ZDOTDIR:-$HOME}/.zshrc
builtin cd -- $__ush_cwd
__ush_run=$USH_RUN
unset USH_RUN __ush_cwd
eval "$__ush_run"
__ush_status=$?
unset __ush_run
if (( __ush_status > 128 )) && [[ $(kill -l $__ush_status) == (TSTP|TTIN|TTOU|STOP) ]]; then
  unset __ush_status
  # The suspended command is the current job (marked +); its first process.
  __ush_job=${${${jobstates[(R)*:+:*]}#*:*:}%%=*}
  __ush_watch() {
    local last=$?
    [[ -n ${jobstates[(R)*:$__ush_job=*]} ]] || exit $last
  }
  [[ -n $__ush_job ]] && precmd_functions=(__ush_watch $precmd_functions)
else
  exit $__ush_status
fi
`

// bash turns job control on only after its rc files, so the command waits
// for the first prompt, in a step that runs ahead of the user's own
// PROMPT_COMMAND and leaves it in place: the prompt after a ^Z is theirs.
// PROMPT_COMMAND is a list only from bash 5.1, where each entry sees the same
// $? and PIPESTATUS; before that the step is a line in front of the user's.
const bashrc = `# Written by ush (cmd/ush/shell.go): your bash, then the shared command.
__ush_cwd=$PWD
[ -r ~/.bashrc ] && . ~/.bashrc
builtin cd -- "$__ush_cwd"
unset __ush_cwd
__ush_step() {
  local status=$?
  if [ -n "${USH_RUN+set}" ]; then
    local run=$USH_RUN
    unset USH_RUN
    eval "$run"
    status=$?
    if [ "$status" -gt 128 ]; then
      case $(kill -l "$status") in TSTP|TTIN|TTOU|STOP)
        # Suspended: the process group of the current job, to look for later.
        __ush_job=$(jobs -p %%)
        return "$status" ;;
      esac
    fi
    exit "$status"
  fi
  if [ -n "$__ush_job" ] && ! kill -0 -- "-$__ush_job" 2>/dev/null; then
    exit "$status"
  fi
  return "$status"
}
if (( BASH_VERSINFO[0] > 5 || (BASH_VERSINFO[0] == 5 && BASH_VERSINFO[1] >= 1) )); then
  PROMPT_COMMAND=(__ush_step "${PROMPT_COMMAND[@]}")
else
  PROMPT_COMMAND="__ush_step${PROMPT_COMMAND:+
$PROMPT_COMMAND}"
fi
`
