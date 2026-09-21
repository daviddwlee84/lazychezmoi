#!/usr/bin/env python3
"""Exercise real terminal input and editor/apply handoff on disposable dotfiles.

Unix uses the OS PTY; Windows uses pywinpty's native ConPTY backend. Nothing is
read from or deployed to the user's chezmoi source, configuration, or home.
"""
import argparse
import codecs
import errno
import json
import os
from pathlib import Path
import queue
import shutil
import subprocess
import sys
import tempfile
import threading
import time


class Terminal:
    def __init__(self, argv, env):
        self.output = ""
        self.chunks = queue.Queue()
        self.code = None
        if os.name == "nt":
            from winpty import PtyProcess
            from winpty.enums import Backend
            self.proc = PtyProcess.spawn(argv, env=env, dimensions=(24, 100), backend=Backend.ConPTY)
            self.reader = lambda: self.proc.read(8192)
        else:
            import pty
            import termios
            ready_read, ready_write = os.pipe()
            pid, fd = pty.fork()
            if pid == 0:
                os.close(ready_write)
                os.read(ready_read, 1)
                os.close(ready_read)
                os.execve(argv[0], argv, env)
            os.close(ready_read)
            self.pid, self.fd = pid, fd
            self.original = termios.tcgetattr(fd)
            self.reader = lambda: os.read(fd, 8192)
            self.resize(24, 100)
            os.write(ready_write, b"1")
            os.close(ready_write)
        self.thread = threading.Thread(target=self._read, daemon=True)
        self.thread.start()

    def _read(self):
        decoder = codecs.getincrementaldecoder("utf-8")(errors="replace")
        try:
            while True:
                data = self.reader()
                if not data:
                    return
                text = decoder.decode(data) if isinstance(data, bytes) else data
                if text:
                    self.chunks.put(text)
        except (EOFError, OSError) as exc:
            if isinstance(exc, OSError) and exc.errno not in (None, errno.EIO, errno.EBADF):
                self.chunks.put(str(exc))

    def drain(self):
        while True:
            try:
                self.output += self.chunks.get_nowait()
            except queue.Empty:
                return

    def wait(self, predicate, label, timeout=15):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            self.drain()
            if predicate():
                return
            if not self.alive():
                break
            time.sleep(0.025)
        self.drain()
        raise AssertionError(f"Timed out: {label}\nTerminal tail:\n{self.output[-8000:]}")

    def contains(self, text):
        self.wait(lambda: text in self.output, text)

    def send(self, text):
        if os.name == "nt":
            self.proc.write(text)
        else:
            os.write(self.fd, text.encode())

    def resize(self, rows, cols):
        if os.name == "nt":
            self.proc.setwinsize(rows, cols)
        else:
            import fcntl
            import struct
            import termios
            fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))

    def alive(self):
        if self.code is not None:
            return False
        if os.name == "nt":
            return self.proc.isalive()
        pid, status = os.waitpid(self.pid, os.WNOHANG)
        if pid:
            self.code = os.waitstatus_to_exitcode(status)
            return False
        return True

    def finish(self):
        end = time.monotonic() + 10
        while self.alive() and time.monotonic() < end:
            self.drain()
            time.sleep(0.025)
        if self.alive():
            self.drain()
            raise AssertionError("TUI did not exit after q\n" + self.output[-6000:])
        self.drain()
        if os.name == "nt":
            code = self.proc.wait()
        else:
            import termios
            code = self.code
            after = termios.tcgetattr(self.fd)
            mask = termios.ECHO | termios.ICANON
            assert after[3] & mask == self.original[3] & mask, "terminal echo/canonical mode not restored"
        assert code == 0, f"TUI exit {code}: {self.output[-4000:]}"

    def close(self):
        if self.alive():
            if os.name == "nt":
                self.proc.terminate(force=True)
            else:
                import signal
                os.kill(self.pid, signal.SIGTERM)
                deadline = time.monotonic() + 2
                while self.alive() and time.monotonic() < deadline:
                    self.drain()
                    time.sleep(0.025)
                if self.alive():
                    os.kill(self.pid, signal.SIGKILL)
                    os.waitpid(self.pid, 0)
                    self.code = -signal.SIGKILL
        if os.name != "nt":
            os.close(self.fd)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("binary", type=lambda s: str(Path(s).resolve()))
    args = parser.parse_args()
    native = shutil.which("chezmoi")
    if not native:
        raise SystemExit("chezmoi is required for the PTY smoke test")
    with tempfile.TemporaryDirectory(prefix="lazychezmoi-pty-") as temp:
        root = Path(temp)
        source, destination = root / "source", root / "destination"
        source.mkdir()
        destination.mkdir()
        (source / "dot_config").mkdir()
        src = source / "dot_config" / "foo.toml"
        target = destination / ".config" / "foo.toml"
        src.write_text("answer = 1\n", encoding="utf-8")
        editor = root / "editor.py"
        editor.write_text("import pathlib,sys\np=pathlib.Path(sys.argv[-1])\np.write_text(p.read_text().replace('= 1','= 2'))\nprint('EDITOR_HANDOFF')\n", encoding="utf-8")
        native_config = root / "chezmoi.toml"
        native_config.write_text(
            "[edit]\ncommand = " + json.dumps(sys.executable) + "\nargs = [" + json.dumps(str(editor)) + "]\n",
            encoding="utf-8",
        )
        own_config = root / "lazychezmoi.toml"
        own_config.write_text("auto_fetch = false\ncolor = 'never'\n", encoding="utf-8")
        native_flags = ["--source", str(source), "--destination", str(destination), "--config", str(native_config), "--cache", str(root / "cache"), "--persistent-state", str(root / "state.db")]
        subprocess.run([native, *native_flags, "apply", "--exclude=scripts"], check=True, capture_output=True)
        subprocess.run(["git", "init", str(source)], check=True, capture_output=True)
        argv = [args.binary, "--chezmoi", native, "--config", str(own_config), "--chezmoi-config", str(native_config), "--source", str(source), "--destination", str(destination), "--chezmoi-cache", str(root / "cache"), "--persistent-state", str(root / "state.db"), "--auto-fetch=false", "--color=never"]
        env = dict(os.environ)
        env["TERM"] = "xterm-256color"
        env["NO_COLOR"] = "1"
        env = {k: v for k, v in env.items() if not k.startswith("LAZYCHEZMOI_RELOAD_")}
        terminal = Terminal(argv, env)
        try:
            terminal.contains("foo.toml")
            terminal.send("/jkhql/?")
            time.sleep(0.15)
            assert terminal.alive(), "typing q in the filter quit the TUI"
            terminal.send("\x1b")
            time.sleep(0.1)
            terminal.send("/foo\r")
            time.sleep(0.1)
            terminal.send("e")
            terminal.contains("EDITOR_HANDOFF")
            terminal.wait(lambda: "= 2" in src.read_text(), "editor changed source")
            terminal.contains("Edit source complete")
            terminal.send("a")
            terminal.contains("Apply selected files complete")
            assert "= 2" in target.read_text(), "selected target applied"
            terminal.send("?")
            time.sleep(0.1)
            terminal.send("\x1b")
            time.sleep(0.15)  # Distinguish a standalone Escape from an Alt chord.
            terminal.resize(18, 55)
            terminal.send("\t\t")
            terminal.resize(24, 80)
            terminal.send("3")
            terminal.contains("Maintenance")
            terminal.send("1")
            time.sleep(0.15)
            terminal.send("q")
            terminal.finish()
        finally:
            terminal.close()
        print("PASS: filter text ownership, editor handoff, scoped apply, help, resize, quit, terminal restoration")


if __name__ == "__main__":
    main()
