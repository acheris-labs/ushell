"""Menu behaviour, driven through real PTYs against the local rendezvous.

Run with:  uv run --no-project python -m unittest test/menu_test.py
Needs the rendezvous up (docker compose up -d, which needs .env from
`make seed`) and bin/ush built.
"""

import fcntl
import os
import shutil
import pty
import re
import select
import signal
import socket
import struct
import subprocess
import tempfile
import termios
import time
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
USH = os.path.join(ROOT, "bin", "ush")
# Which rendezvous to drive: the container on 7874 by default, or a local ushd.
PORT = os.environ.get("USH_TEST_PORT", "7874")
ANSI = re.compile(r"\x1b\[[0-9;?]*[a-zA-Z]|\x1b[()][0-9A-Za-z]")


class Term:
    """A process on a real PTY with a real window size (fzf panics without one)."""

    def __init__(self, argv, env):
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            os.environ.update(env)
            os.execv(argv[0], argv)
        fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 120, 0, 0))
        os.set_blocking(self.fd, False)
        self.raw = b""

    def read(self, seconds):
        end = time.time() + seconds
        while time.time() < end:
            ready, _, _ = select.select([self.fd], [], [], 0.1)
            if not ready:
                continue
            try:
                chunk = os.read(self.fd, 65536)
            except OSError:
                break
            if not chunk:
                break
            self.raw += chunk
        return self.text

    def wait_for(self, needle, seconds):
        end = time.time() + seconds
        while time.time() < end:
            if needle in self.read(0.2):
                return True
        return False

    def send(self, data):
        os.write(self.fd, data)

    @property
    def text(self):
        return ANSI.sub("", self.raw.decode(errors="replace"))

    def alive(self):
        return os.waitpid(self.pid, os.WNOHANG) == (0, 0)

    def kill(self):
        # The whole process group, as closing a terminal does: ush runs ssh
        # as a child, which outlives a kill aimed at ush alone.
        # macOS refuses (EPERM) once the leader has exited, and by then so
        # has ssh.
        try:
            os.killpg(self.pid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass
        try:
            os.waitpid(self.pid, 0)
        except ChildProcessError:
            pass


def M_ANSI(raw):
    return ANSI.sub("", raw.decode(errors="replace"))


class MenuTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        # Same setup a user does: derive the key from the rendezvous's seed.
        cls.tmp = tempfile.TemporaryDirectory()
        rc = os.path.join(cls.tmp.name, "ushrc")
        cls.key = os.path.join(cls.tmp.name, "psk")
        with open(os.path.join(ROOT, ".env")) as f:
            seed = next(l.split("=", 1)[1].strip() for l in f if l.startswith("USH_SEED="))
        subprocess.run(
            [USH, "init", "-y", "--seed", "--profile", "local", "--rendezvous", "127.0.0.1",
             "--port", PORT, "--key", cls.key,
             "--skip-host-key-check"],
            env={**os.environ, "USH_RC": rc, "USH_SEED": seed},
            check=True, capture_output=True,
        )
        # USH_SESSION would make ush refuse to share: these tests may well run
        # from inside a shared terminal, which is exactly what that guard is
        # for, but here the harness is the thing being tested.
        # A short socket directory: a unix socket path is about 100 bytes, and
        # the default temp directories are longer than that.
        cls.keepdir = tempfile.mkdtemp(prefix="/tmp/ushk")
        cls.env = {"USH_RC": rc, "TERM": "xterm-256color", "PS1": "$ ", "USH_SESSION": "",
                   "USH_KEEP_DIR": cls.keepdir}

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def setUp(self):
        self.terms = []

    def tearDown(self):
        for t in self.terms:
            t.kill()

    def term(self, *args):
        t = Term([USH, *args], self.env)
        self.terms.append(t)
        return t

    def share(self, name):
        sharer = self.term("share", "-n", name, "--", "/bin/sh")
        self.assertTrue(self.registered(name), f"{name} never registered")
        return sharer

    def gone(self, name, seconds=10):
        """Wait for a session to leave the registry."""
        end = time.time() + seconds
        while time.time() < end:
            if not self.registered(name, seconds=1):
                return
            time.sleep(0.3)
        raise AssertionError(f"{name} never left the registry")

    def registered(self, name, seconds=10):
        end = time.time() + seconds
        while time.time() < end:
            out = subprocess.run(
                ["ssh", "-i", self.key, "-p", PORT, "-o", "StrictHostKeyChecking=no",
                 "-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR",
                 "ush@127.0.0.1", "list"],
                capture_output=True, text=True,
            ).stdout
            if f'"name": "{name}"' in out:
                return True
            time.sleep(0.3)
        return False

    def test_session_that_ends_is_reported_as_ended_not_stale(self):
        self.share("ending")
        menu = self.term()
        self.assertTrue(menu.wait_for("ending", 8), menu.text)
        menu.send(b"\r")
        time.sleep(2)
        menu.send(b"exit\r")  # ends the shared command, and so the session
        self.assertTrue(menu.wait_for('session "ending" ended', 8), menu.text)
        self.assertNotIn("stale", menu.text)
        self.assertNotIn("pruned", menu.text)
        self.assertTrue(menu.alive(), "menu should survive the session ending")

    def test_ended_notice_shows_above_remaining_sessions(self):
        self.share("stays")
        self.share("goes")
        menu = self.term()
        self.assertTrue(menu.wait_for("stays", 8), menu.text)
        menu.send(b"goes")  # fzf filter: select "goes", not "stays"
        time.sleep(1)
        menu.send(b"\r")
        time.sleep(2)
        menu.send(b"exit\r")
        self.assertTrue(menu.wait_for('session "goes" ended', 8), menu.text)
        before = len(menu.text)
        menu.read(2)
        self.assertIn("stays", menu.text[before - 2000:], "remaining session should be listed")

    def test_detach_key_returns_to_menu_and_session_survives(self):
        sharer = self.share("detachme")
        menu = self.term()
        self.assertTrue(menu.wait_for("detachme", 8), menu.text)
        menu.send(b"\r")
        time.sleep(2)
        menu.send(b"abc\x1d")  # Ctrl-] mid-line: no Enter first
        self.assertTrue(menu.wait_for("back to list", 8), "Ctrl-] should open the panel")
        menu.send(b"l")
        self.assertTrue(menu.wait_for('left "detachme", still running', 8), menu.text)
        self.assertTrue(sharer.alive(), "detaching must not end the sharer's session")
        self.assertTrue(menu.alive(), "detaching must return to the menu, not exit")

        menu.send(b"\r")  # rejoin the same session from the list
        time.sleep(2)
        menu.send(b"\x15echo rejoined-$((6*7))\r")  # Ctrl-U clears the "abc"
        self.assertTrue(menu.wait_for("rejoined-42", 8), menu.text)

    def test_window_size_and_resizes_reach_the_session(self):
        # Full-screen programs (claude, vi) redraw from this.
        self.share("resizeme")
        menu = self.term()
        self.assertTrue(menu.wait_for("resizeme", 8), menu.text)
        menu.send(b"\r")
        time.sleep(2)
        menu.send(b"stty size\r")
        self.assertTrue(menu.wait_for("40 120", 5), "initial size not forwarded")
        fcntl.ioctl(menu.fd, termios.TIOCSWINSZ, struct.pack("HHHH", 30, 100, 0, 0))
        time.sleep(2)
        menu.send(b"stty size\r")
        self.assertTrue(menu.wait_for("30 100", 5), "resize not forwarded")

    def test_preview_is_a_readable_card_not_json(self):
        self.share("carded")
        menu = self.term()
        self.assertTrue(menu.wait_for("carded", 8), menu.text)
        menu.read(2)  # let the preview pane render
        text = menu.text
        for label in ("machine", "command", "cwd", "access", "started", "ago"):
            self.assertIn(label, text, f"preview is missing {label!r}")
        self.assertIn("can type", text)
        self.assertNotIn('"session_id"', text, "preview still shows raw JSON")
        self.assertNotIn("started_epoch", text, "preview still shows the raw epoch")
        # The terminal must not be queried: its reply would arrive as keystrokes.
        self.assertNotIn(b"\x1b]11;?", menu.raw, "menu asked the terminal for its background colour")

    def test_open_list_refreshes_by_itself(self):
        self.share("early")
        menu = self.term()
        self.assertTrue(menu.wait_for("early", 8), menu.text)
        self.share("late")
        # No keypress: the open list must pick up the new session.
        self.assertTrue(menu.wait_for("late", 10), "new session never appeared")
        # A typed filter must survive the refreshes that keep happening.
        menu.send(b"late")
        time.sleep(5)
        menu.send(b"\r")
        time.sleep(2)
        menu.send(b"echo joined=$USH_SESSION\r")
        self.assertTrue(menu.wait_for("joined=late", 8), "filter lost across refreshes")

    def test_ctrl_r_refreshes_the_open_list(self):
        self.share("early")
        menu = self.term()
        self.assertTrue(menu.wait_for("early", 8), menu.text)
        self.share("late")
        menu.send(b"\x12")  # Ctrl-R
        self.assertTrue(menu.wait_for("late", 8), "Ctrl-R did not pick up the new session")

    def join_first(self, menu, name):
        self.assertTrue(menu.wait_for(name, 8), menu.text)
        menu.send(b"\r")
        time.sleep(2)

    def test_panel_back_resumes_the_session(self):
        self.share("resumer")
        menu = self.term()
        self.join_first(menu, "resumer")
        menu.send(b"\x1d")
        self.assertTrue(menu.wait_for("back to session", 8), menu.text)
        menu.send(b"b")
        time.sleep(1)
        menu.send(b"echo resumed-$((6*7))\r")
        self.assertTrue(menu.wait_for("resumed-42", 8), "session did not resume after the panel")

    def test_panel_opens_from_the_kitty_key_encoding(self):
        # A program that turns on the Kitty keyboard protocol -- herdr pushes
        # flags 7 -- makes terminals that support it report Ctrl-] as an escape
        # sequence instead of 0x1d. Terminals that do not (ShellFish) keep
        # sending the byte, so both have to open the panel.
        self.share("kittykey")
        menu = self.term()
        self.join_first(menu, "kittykey")
        menu.send(b"\x1b[93;5u")
        self.assertTrue(menu.wait_for("back to session", 8),
                        "Ctrl-] in its escape-sequence form did not open the panel")
        menu.send(b"b")
        time.sleep(1)
        menu.send(b"echo back-$((6*7))\r")
        self.assertTrue(menu.wait_for("back-42", 8), "session did not resume")

    def test_panel_counts_viewers(self):
        self.share("watched")
        first = self.term()
        self.join_first(first, "watched")
        second = self.term()
        self.join_first(second, "watched")
        second.send(b"\x1d")
        self.assertTrue(second.wait_for("2 (incl. you)", 8), second.text[-600:])

    def test_menu_and_list_count_viewers(self):
        self.share("busy")
        viewer = self.term()
        self.join_first(viewer, "busy")
        menu = self.term()
        self.assertTrue(menu.wait_for("busy", 8), menu.text)
        menu.send(b"busy")  # select it, so the card is busy's
        self.assertTrue(menu.wait_for("1 joined", 8), menu.text[-800:])
        self.assertIn("VIEWERS", menu.text)
        out = subprocess.run(
            ["ssh", "-i", self.key, "-p", PORT, "-o", "StrictHostKeyChecking=no",
             "-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR",
             "ush@127.0.0.1", "list"],
            capture_output=True, text=True,
        ).stdout
        self.assertIn('"viewers": 1', out)

    def test_panel_rename_shows_in_the_list(self):
        self.share("oldname")
        menu = self.term()
        self.join_first(menu, "oldname")
        menu.send(b"\x1d")
        self.assertTrue(menu.wait_for("rename", 8), menu.text)
        menu.send(b"n")
        time.sleep(0.5)
        menu.send(b"newname\r")
        self.assertTrue(menu.wait_for("renamed", 8), menu.text[-600:])
        menu.send(b"l")
        self.assertTrue(menu.wait_for('left "newname"', 8), menu.text[-600:])

    def test_panel_refresh_repaints_and_resumes(self):
        # Ctrl-] r nudges the window size so the program redraws; on a bare
        # shell there is nothing to redraw, so check it returns to the session.
        self.share("refresher")
        menu = self.term()
        self.join_first(menu, "refresher")
        menu.send(b"\x1d")
        self.assertTrue(menu.wait_for("refresh", 8), menu.text)
        menu.send(b"r")
        time.sleep(1)
        menu.send(b"echo refreshed-$((6*7))\r")
        self.assertTrue(menu.wait_for("refreshed-42", 8), "session did not resume after refresh")

    def test_leaving_restores_the_terminal(self):
        # The session paints on the alternate screen and ush pops back out of
        # it on the way out, so the terminal is handed back as it was found.
        self.share("tidy")
        menu = self.term()
        self.join_first(menu, "tidy")
        self.assertIn(b"\x1b[?1049h", menu.raw, "session did not use the alternate screen")
        menu.send(b"\x1d")
        self.assertTrue(menu.wait_for("disconnect", 8), menu.text)
        menu.send(b"d")
        menu.read(3)
        # One sequence, written as the session hands the terminal back. The
        # menu's own bubbletea shutdown looks similar, so match ours exactly.
        # Includes the Kitty keyboard protocol pop: a program that pushed its
        # own key encoding must not leave the terminal in it.
        restore = (b"\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1005l\x1b[?1006l\x1b[?1015l"
                   b"\x1b[?1004l\x1b[?2004l\x1b[<u\x1b[?25h\x1b[0m\x1b[?1049l")
        self.assertIn(restore, menu.raw, "the session did not restore the terminal on the way out")

    def test_panel_leaves_a_repainted_session(self):
        # The panel draws on the same alternate screen, which clears on entry,
        # so coming back has to make the program repaint.
        if not shutil.which("tmux"):
            self.skipTest("tmux not installed")
        name = "panelrepaint"
        sharer = self.term("share", "-k", "--mux", "tmux", "-n", name)
        self.assertTrue(self.registered(name), "share never registered")
        self.addCleanup(subprocess.run, ["tmux", "kill-session", "-t", "=" + name],
                        capture_output=True)
        time.sleep(4)
        sharer.send(b"echo MARKER-ON-SCREEN\r")
        self.assertTrue(sharer.wait_for("MARKER-ON-SCREEN", 8), "marker never reached the session")
        menu = self.term()
        self.join_first(menu, name)
        self.assertTrue(menu.wait_for("MARKER-ON-SCREEN", 10), menu.text[-600:])
        menu.send(b"\x1d")
        self.assertTrue(menu.wait_for("back to session", 8), menu.text[-600:])
        menu.send(b"b")
        seen = len(menu.raw)
        menu.read(4)
        after = ANSI.sub("", menu.raw[seen:].decode(errors="replace"))
        self.assertIn("MARKER-ON-SCREEN", after, "session did not repaint after the panel")

    def test_panel_disconnect_quits(self):
        for key in (b"d", b"q"):
            with self.subTest(key=key):
                self.share("leaving")
                menu = self.term()
                self.join_first(menu, "leaving")
                menu.send(b"\x1d")
                self.assertTrue(menu.wait_for("disconnect", 8), menu.text)
                menu.send(key)
                menu.read(3)
                self.assertFalse(menu.alive(), "disconnect should end ush entirely")

    def test_viewer_sees_what_was_on_screen_before_joining(self):
        # A viewer only receives what is written after joining, so ush nudges
        # the window size on attach to make the program repaint. Needs a
        # program that repaints: a bare shell has nothing to redraw.
        if not shutil.which("tmux"):
            self.skipTest("tmux not installed")
        name = "repaint"
        sharer = self.term("share", "-k", "--mux", "tmux", "-n", name)
        self.assertTrue(self.registered(name), "share never registered")
        self.addCleanup(subprocess.run, ["tmux", "kill-session", "-t", "=" + name],
                        capture_output=True)
        time.sleep(4)  # let the attach finish, or the keystrokes are lost
        sharer.send(b"echo MARKER-BEFORE-JOIN\r")
        self.assertTrue(sharer.wait_for("MARKER-BEFORE-JOIN", 8), "marker never reached the session")
        menu = self.term()
        self.pick_first(menu, name)
        self.assertTrue(menu.wait_for("MARKER-BEFORE-JOIN", 10),
                        "viewer never saw the screen from before it joined")

    def pick_first(self, menu, name):
        self.assertTrue(menu.wait_for(name, 8), menu.text)
        menu.send(b"\r")
        time.sleep(2)

    def test_keep_viewer_gets_its_own_client(self):
        # The plain shared pty is sized to the smallest terminal on it, so a
        # small viewer used to shrink the sharer's window -- and tmux, drawing
        # deltas into that shared pty, sent viewers nothing at all. In keep
        # mode each viewer runs its own `tmux attach` instead.
        if not shutil.which("tmux"):
            self.skipTest("tmux not installed")
        name = "ownclient"
        sharer = self.term("share", "-k", "--mux", "tmux", "-n", name)
        self.assertTrue(self.registered(name), "share never registered")
        self.addCleanup(subprocess.run, ["tmux", "kill-session", "-t", "=" + name],
                        capture_output=True)
        time.sleep(4)
        menu = self.term()
        self.join_first(menu, name)
        menu.send(b"echo LIVE-OUTPUT-$((6*7))\r")
        self.assertTrue(menu.wait_for("LIVE-OUTPUT-42", 8), "viewer saw no live output")

        # The viewer shrinks; the sharer's window must survive it.
        fcntl.ioctl(menu.fd, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))
        time.sleep(2)
        menu.send(b"\x1d")
        self.assertTrue(menu.wait_for("disconnect", 8), menu.text[-400:])
        menu.send(b"d")
        menu.read(3)
        time.sleep(2)
        seen = len(sharer.raw)
        sharer.send(b"stty size\r")
        sharer.read(3)
        sizes = [l.strip() for l in M_ANSI(sharer.raw[seen:]).splitlines() if l.strip()[:2].isdigit()]
        self.assertTrue(sizes, "no size reported by the sharer")
        rows, cols = (int(x) for x in sizes[-1].split())
        self.assertEqual(cols, 120, f"the viewer's width stuck to the sharer: {sizes[-1]}")
        self.assertGreaterEqual(rows, 39, f"the viewer's height stuck to the sharer: {sizes[-1]}")

    def test_keep_viewer_is_live_on_herdr_too(self):
        # herdr viewers received nothing at all while they shared the sharer's
        # pty: the screen only changed when something forced a full repaint.
        if not shutil.which("herdr"):
            self.skipTest("herdr not installed")
        name = "herdrlive"
        self.term("share", "-k", "--mux", "herdr", "-n", name)
        self.assertTrue(self.registered(name), "share never registered")
        self.addCleanup(subprocess.run, ["herdr", "session", "delete", name], capture_output=True)
        self.addCleanup(subprocess.run, ["herdr", "session", "stop", name], capture_output=True)
        time.sleep(7)  # herdr takes a moment to come up
        menu = self.term()
        self.join_first(menu, name)
        menu.send(b"echo HERDR-LIVE-$((6*7))\r")
        self.assertTrue(menu.wait_for("HERDR-LIVE-42", 15), "herdr viewer saw no live output")

    def test_a_kept_session_outlives_the_terminal_that_shared_it(self):
        # -k keeps the session in a process of ush's own by default, and that
        # process holds the share too: closing the window that started it must
        # leave the session running *and* listed, or keeping it was pointless.
        name = "outlives"
        sharer = self.term("share", "-k", "-n", name, "--", "sleep", "600")
        # This session deliberately outlives its terminal, so it has to be
        # taken down by hand -- and the next test must not find it listed.
        # It outlives its terminal by design, so it has to be stopped by name,
        # and the tests that follow must not find it in the registry.
        self.addCleanup(self.gone, name)
        self.addCleanup(subprocess.run, [USH, "stop", name],
                        env={**os.environ, **self.env}, capture_output=True)
        self.assertTrue(self.registered(name), "the kept session never registered")

        os.close(sharer.fd)  # what closing the tab does
        self.terms.remove(sharer)
        time.sleep(3)

        self.assertTrue(self.registered(name, seconds=5),
                        "the session left the menu when its terminal closed")
        kept = subprocess.run([USH, "attach", "--list"],
                              env={**os.environ, **self.env}, capture_output=True, text=True).stdout
        self.assertIn(name, kept, "the session itself should still be kept")

    def test_empty_menu_refreshes_by_itself(self):
        menu = self.term()
        self.assertTrue(menu.wait_for("No active sessions", 8), menu.text)
        self.share("latecomer")
        # No keypress: the waiting screen must notice the new session on its own.
        self.assertTrue(menu.wait_for("latecomer", 10), menu.text)

    def test_q_quits_the_empty_menu(self):
        menu = self.term()
        self.assertTrue(menu.wait_for("No active sessions", 8), menu.text)
        menu.send(b"q")
        menu.read(3)
        self.assertFalse(menu.alive(), "q should end the menu")

    def test_connect_by_name_joins_without_the_menu(self):
        sharer = self.share("direct")
        viewer = self.term("connect", "direct")
        time.sleep(2)
        viewer.send(b"echo direct-$((6*7))\r")
        self.assertTrue(viewer.wait_for("direct-42", 8), viewer.text)
        self.assertNotIn("enter joins", viewer.text, "connect NAME should not show the menu")
        viewer.send(b"\x1d")
        self.assertTrue(viewer.wait_for("disconnect", 8), viewer.text)
        viewer.send(b"d")
        viewer.read(3)
        self.assertFalse(viewer.alive(), "disconnect should end ush")
        self.assertTrue(sharer.alive(), "disconnecting must not end the sharer's session")

    def test_connect_back_to_list_opens_the_menu(self):
        self.share("tolist")
        viewer = self.term("connect", "tolist")
        time.sleep(2)
        viewer.send(b"\x1d")
        self.assertTrue(viewer.wait_for("back to list", 8), viewer.text)
        viewer.send(b"l")
        self.assertTrue(viewer.wait_for("enter joins", 8), viewer.text)
        self.assertTrue(viewer.alive(), "back to list should leave the menu open")

    def test_connect_alone_is_the_menu(self):
        self.share("plainmenu")
        menu = self.term("connect")
        self.assertTrue(menu.wait_for("enter joins", 8), menu.text)
        self.assertTrue(menu.wait_for("plainmenu", 8), menu.text)

    def test_connect_to_a_shared_name_lists_ids(self):
        self.share("twin")
        self.share("twin")
        viewer = self.term("connect", "twin")
        self.assertTrue(viewer.wait_for('2 sessions are named "twin"', 8), viewer.text)
        self.assertIn("ush connect ", viewer.text)

    def test_connect_picks_by_machine(self):
        self.share("onmachine")
        # Every share here is from this machine, under its short hostname;
        # -m matches it whatever the case.
        machine = socket.gethostname().split(".")[0].upper()
        viewer = self.term("connect", "onmachine", "-m", machine)
        time.sleep(2)
        viewer.send(b"echo machine-$((6*7))\r")
        self.assertTrue(viewer.wait_for("machine-42", 8), viewer.text)

        elsewhere = self.term("connect", "onmachine", "-m", "elsewhere")
        self.assertTrue(elsewhere.wait_for('no session named "onmachine": no sessions are live on "elsewhere"', 8),
                        elsewhere.text)

    def test_connect_list_prints_live_sessions(self):
        self.share("listed")
        # Inside a shared session too: listing takes nothing over.
        out = subprocess.run([USH, "connect", "--list"], env={**os.environ, **self.env, "USH_SESSION": "outer"},
                             capture_output=True, text=True, timeout=15)
        self.assertEqual(out.returncode, 0, out.stderr)
        self.assertTrue(out.stdout.startswith("NAME"), out.stdout)
        row = next((l for l in out.stdout.splitlines() if l.startswith("listed ")), None)
        self.assertIsNotNone(row, out.stdout)
        self.assertRegex(row, r"\b[A-Za-z0-9]{20}$")

        out = subprocess.run([USH, "connect", "--list", "-m", "elsewhere"], env={**os.environ, **self.env},
                             capture_output=True, text=True, timeout=15)
        self.assertEqual(out.stdout, 'no sessions are live on "elsewhere"\n', out.stderr)

    def test_connect_to_a_missing_name_says_so(self):
        viewer = self.term("connect", "nosuchsession")
        self.assertTrue(viewer.wait_for('no session named "nosuchsession"', 8), viewer.text)


if __name__ == "__main__":
    unittest.main()
