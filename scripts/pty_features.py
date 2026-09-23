#!/usr/bin/env python3
"""Real PTY/ConPTY checks for mouse, hunk copies and live grep (requires pyte)."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import queue

from pty_smoke import Terminal
from terminal_screen import Screen, Stream


class FeatureTerminal(Terminal):
    def __init__(self, argv, env):
        self.screen = Screen(100, 24)
        self.stream = Stream(self.screen)
        self.last_output = time.monotonic()
        super().__init__(argv, env)

    def drain(self):
        while True:
            try:
                data = self.chunks.get_nowait()
                self.output += data
                self.stream.feed(data)
                self.last_output = time.monotonic()
            except queue.Empty:
                return

    def resize(self, rows, cols):
        self.screen.resize(lines=rows, columns=cols)
        super().resize(rows, cols)

    def visible(self, text):
        try:
            self.wait(lambda: text in "\n".join(self.screen.display), f"visible {text}")
        except AssertionError:
            print("\n".join(f"{i + 1:02}: {row}" for i, row in enumerate(self.screen.display)), flush=True)
            raise

    def click_text(self, text):
        self.visible(text)
        for y, row in enumerate(self.screen.display):
            x = row.find(text)
            if x >= 0:
                print(f"Click {text} at {x + 1},{y + 1}", flush=True)
                self.click(x + 1, y + 1)
                return
        raise AssertionError(f"No mouse target: {text}")

    def click(self, x, y):
        self.send(f"\x1b[<0;{x};{y}M\x1b[<0;{x};{y}m")

    def ready_hunks(self, header):
        def ready():
            screen = "\n".join(self.screen.display)
            # A PTY read can stop between the new header and the loading/body
            # updates of one frame. Wait for the selected fixture's actual
            # rendered diff and a quiet screen before sending a guarded action.
            contents = {
                "Hunk 1/2": ("-answer = 1", "+answer = 2"),
                "Hunk 2/2": ("-tail = 1", "+tail = 2"),
            }
            rendered = any(label in screen and all(line in screen for line in lines)
                           for label, lines in contents.items())
            return (header in screen and rendered
                    and not any(marker in screen for marker in ("Loading preview", "BUSY", "stale"))
                    and time.monotonic() - self.last_output >= 0.1)
        self.wait(ready, f"rendered and settled {header}")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("binary", type=lambda value: str(Path(value).resolve()))
    args = parser.parse_args()
    native, rg = shutil.which("chezmoi"), shutil.which("rg")
    if not native or not rg:
        raise SystemExit("chezmoi and rg are required for feature acceptance")
    with tempfile.TemporaryDirectory(prefix="lazychezmoi-features-") as temp:
        root = Path(temp)
        source, destination, config = root / "source", root / "destination", root / "config"
        for path in (source, destination, config):
            path.mkdir()
        shared = "".join(f"# same {n}\n" for n in range(8))
        source_text = "answer = 2\n" + shared + "tail = 2\n"
        current_text = "answer = 1\n" + shared + "tail = 1\n"
        source_file, current_file = source / "dot_demo.toml", destination / ".demo.toml"
        source_file.write_text(source_text, encoding="utf-8")
        current_file.write_text(current_text, encoding="utf-8")
        (source / ".chezmoitemplates").mkdir()
        helper = source / ".chezmoitemplates" / "helper.tmpl"
        helper.write_text("helperneedle\n", encoding="utf-8")
        (destination / "unmanaged.txt").write_text("answer = 999\n", encoding="utf-8")
        editor = root / "editor.py"
        editor.write_text("import pathlib,sys\np=pathlib.Path(sys.argv[-1])\np.write_text(p.read_text().replace('helperneedle','helper edited'))\nprint('SEARCH_EDITOR_HANDOFF')\n", encoding="utf-8")
        native_config, own_config = config / "chezmoi.toml", config / "lazychezmoi.toml"
        native_config.write_text("[edit]\ncommand=" + json.dumps(sys.executable) + "\nargs=[" + json.dumps(str(editor)) + "]\n", encoding="utf-8")
        own_config.write_text("auto_fetch=false\nmouse=true\n", encoding="utf-8")
        env = {k: v for k, v in os.environ.items() if not k.startswith("LAZYCHEZMOI_RELOAD_")}
        env.update(TERM="xterm-256color", GIT_CONFIG_GLOBAL=str(root / "empty-gitconfig"), GIT_CONFIG_NOSYSTEM="1")
        subprocess.run(["git", "init", str(source)], check=True, capture_output=True, env=env)
        argv = [args.binary, "--config", str(own_config), "--chezmoi-config", str(native_config), "--chezmoi", native, "--rg", rg, "--source", str(source), "--destination", str(destination), "--chezmoi-cache", str(root / "cache"), "--persistent-state", str(root / "state.db"), "--auto-fetch=false", "--color=always"]
        terminal = FeatureTerminal(argv, env)
        try:
            terminal.resize(30, 160)
            terminal.visible(".demo.toml")
            terminal.contains("\x1b[?1002h")
            terminal.send("H")
            terminal.ready_hunks("Hunk 1/2")
            terminal.visible("Renderer:")
            terminal.click_text("[Next]")
            terminal.ready_hunks("Hunk 2/2")
            terminal.click_text("[< Source")
            terminal.visible("Review")
            terminal.click(4, 3)  # A modal must consume clicks on background tabs.
            assert current_file.read_text() == current_text, "copy ran without confirmation"
            terminal.click_text("[Confirm]")
            # Observe completion before opening receiving files: polling them
            # during ReplaceFileW can collide with Windows sharing semantics.
            terminal.visible("Copy hunk Source → Current complete")
            assert current_file.read_text() == "answer = 1\n" + shared + "tail = 2\n", "only the selected hunk was copied"
            terminal.send("U")
            terminal.visible("Undo hunk copy complete")
            assert current_file.read_text() == current_text, "undo restored current bytes"
            terminal.ready_hunks("Hunk ")
            # Wait for a different selection before navigating back. Sending
            # N while already on hunk 1 lets the old header satisfy the wait
            # before the new asynchronous render is ready for a review.
            if "Hunk 1/2" in "\n".join(terminal.screen.display):
                terminal.send("n")
            terminal.ready_hunks("Hunk 2/2")
            terminal.send("N")
            terminal.ready_hunks("Hunk 1/2")
            terminal.send(">")
            terminal.visible("Review")
            terminal.send("\r")
            terminal.visible("Copy hunk Current → Source complete")
            assert source_file.read_text() == "answer = 1\n" + shared + "tail = 2\n", "current-to-source copied the selected hunk"
            terminal.send("U")
            terminal.visible("Undo hunk copy complete")
            assert source_file.read_text() == source_text, "undo restored source bytes"

            terminal.send("shelperneedle")
            terminal.visible("helper.tmpl")  # Debounce must complete before Enter.
            terminal.visible("Repository helper")
            terminal.send("\r")
            terminal.send("e")
            terminal.contains("SEARCH_EDITOR_HANDOFF")
            terminal.wait(lambda: helper.read_text() == "helper edited\n", "unmanaged helper editor")
            terminal.visible("complete")
            terminal.send("s\x15answer")
            # The Search header may already be on screen from the previous
            # query. Wait for this query's result before accepting/clicking.
            terminal.visible("dot_demo.toml:1:1")
            terminal.visible("Scope: source")
            terminal.send("\r")
            terminal.wait(lambda: "Typing searches" not in "\n".join(terminal.screen.display), "accepted search input")
            terminal.click_text("Current")
            terminal.visible("Scope: current")
            terminal.visible("1 matches")
            terminal.send("\x1b[<65;120;15M")  # Real SGR wheel input over preview.
            terminal.send("m")
            terminal.visible("Mouse capture disabled")
            terminal.contains("\x1b[?1002l")
            terminal.send("m")
            terminal.visible("Mouse capture enabled")
            terminal.resize(18, 55)
            terminal.send("z")
            time.sleep(0.1)
            terminal.resize(24, 80)
            terminal.send("q")
            terminal.finish()
            assert terminal.output.rfind("\x1b[?1002l") > terminal.output.rfind("\x1b[?1002h"), "mouse reporting not restored on exit"
        finally:
            terminal.close()
        print("PASS: SGR mouse/hunk navigation, modal confirmation, both copy directions and Undo, live grep, helper editor, Current allowlist, mouse cleanup")


if __name__ == "__main__":
    main()
