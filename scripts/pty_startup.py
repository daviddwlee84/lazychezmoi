#!/usr/bin/env python3
"""Measure usable startup through a real PTY with an isolated, slow status scan.

Requires Go, chezmoi, git, rg and pyte (plus pywinpty on Windows). The native proxy
is compiled in a disposable directory so Windows never depends on executing a
shebang script. Only fixture paths are passed to chezmoi; status is delayed five
seconds by default. Output contains timings and command counts, never config or
rendered file contents. --baseline measures older binaries without requiring
their inventory to arrive before status finishes.
"""
import argparse
from collections import Counter
import json
import os
from pathlib import Path
import queue
import re
import shutil
import subprocess
import sys
import tempfile
import time

try:
    import pyte
except ImportError:
    raise SystemExit("pty_startup.py requires pyte==0.8.2") from None

from pty_smoke import Terminal


PROXY_SOURCE = r'''package main
import (
    "encoding/json"
    "os"
    "os/exec"
    "time"
)
func record(command, phase string, code int) {
    f, err := os.OpenFile(os.Getenv("LAZYCHEZMOI_STARTUP_LOG"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
    if err != nil { os.Exit(125) }
    defer f.Close()
    body, _ := json.Marshal(map[string]any{"command":command,"phase":phase,"code":code,"pid":os.Getpid(),"time_ns":time.Now().UnixNano()})
    body = append(body, '\n')
    if _, err := f.Write(body); err != nil { os.Exit(125) }
}
func main() {
    command := "unknown"
    for _, arg := range os.Args[1:] {
        switch arg {
        case "dump-config", "source-path", "execute-template", "managed", "status", "edit", "diff", "cat", "data", "state":
            command = arg
        case "--version": command = "version"
        }
    }
    record(command, "start", 0)
    if command == "status" {
        delay, err := time.ParseDuration(os.Getenv("LAZYCHEZMOI_STARTUP_DELAY"))
        if err != nil { os.Exit(125) }
        time.Sleep(delay)
    }
    child := exec.Command(os.Getenv("LAZYCHEZMOI_STARTUP_NATIVE"), os.Args[1:]...)
    child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
    code := 0
    err := child.Start()
    if err == nil {
        record(command, "child", child.Process.Pid)
        err = child.Wait()
    }
    if err != nil {
        code = 1
        if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() >= 0 { code = exit.ExitCode() }
    }
    record(command, "end", code)
    os.Exit(code)
}
'''


class StartupScreen(pyte.Screen):
    # Bubble Tea optimizes pane scrolling with CSI S/T. pyte 0.8.2 does not
    # implement those controls, so add their scroll-region behavior explicitly.
    # Cursor position remains unchanged, as it does in the real terminal.
    def scroll_up(self, count=None):
        top, bottom = self.margins or (0, self.lines - 1)
        self.dirty.update(range(top, bottom + 1))
        for _ in range(min(count or 1, bottom - top + 1)):
            for y in range(top, bottom):
                self.buffer[y] = self.buffer[y + 1]
            self.buffer.pop(bottom, None)

    def scroll_down(self, count=None):
        top, bottom = self.margins or (0, self.lines - 1)
        self.dirty.update(range(top, bottom + 1))
        for _ in range(min(count or 1, bottom - top + 1)):
            for y in range(bottom, top, -1):
                self.buffer[y] = self.buffer[y - 1]
            self.buffer.pop(top, None)


class StartupStream(pyte.Stream):
    csi = {**pyte.Stream.csi, "S": "scroll_up", "T": "scroll_down"}
    events = pyte.Stream.events | {"scroll_up", "scroll_down"}


class StartupTerminal(Terminal):
    def __init__(self, argv, env, started):
        self.screen = StartupScreen(100, 24)
        self.stream = StartupStream(self.screen)
        self.started = started
        self.milestones = {}
        self.query_buffer = ""
        self.query_counts = Counter()
        super().__init__(argv, env)

    def drain(self):
        while True:
            try:
                data = self.chunks.get_nowait()
            except queue.Empty:
                return
            self.output += data
            self.stream.feed(data)
            self.reply_to_queries(data)
            visible = self.visible_text()
            for name, found in (
                ("first_frame", "lazychezmoi" in visible),
                ("usable_inventory", "alpha.toml" in visible and "bravo.toml" in visible),
                ("source_preview", "STARTUP_ALPHA_LINE_00" in visible),
            ):
                if found and name not in self.milestones:
                    self.milestones[name] = time.perf_counter() - self.started

    def reply_to_queries(self, data):
        # pyte parses display controls but is not an actual responding terminal.
        # Native interactive chezmoi requests background/cursor information
        # before invoking its editor. Retain incomplete sequences across reads,
        # consume completed queries once, and answer on the PTY input side.
        self.query_buffer += data
        end = 0
        for match in re.finditer(r"\x1b\]11;\?(?:\x07|\x1b\\)|\x1b\[6n", self.query_buffer):
            if match.group().startswith("\x1b]"):
                self.send("\x1b]11;rgb:0000/0000/0000\x1b\\")
                self.query_counts["background"] += 1
            else:
                row = min(self.screen.lines, self.screen.cursor.y + 1)
                column = min(self.screen.columns, self.screen.cursor.x + 1)
                self.send(f"\x1b[{row};{column}R")
                self.query_counts["cursor"] += 1
            end = match.end()
        self.query_buffer = self.query_buffer[end:][-64:]

    def resize(self, rows, cols):
        self.screen.resize(lines=rows, columns=cols)
        super().resize(rows, cols)

    def visible_text(self):
        return "\n".join(self.screen.display)

    def wait(self, predicate, label, timeout=15):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            self.drain()
            if predicate():
                return
            if not self.alive():
                break
            time.sleep(0.01)
        self.drain()
        # Do not include terminal/config/source bodies in benchmark output.
        raise AssertionError("Timed out: " + label)

    def settled_markers(self, pattern, first=None, timeout=2):
        previous, changed = [], time.monotonic()

        def settled():
            nonlocal previous, changed
            visible = self.visible_text()
            markers = re.findall(pattern, visible)
            ready = (markers and (first is None or markers[0] == first)
                     and "Loading preview" not in visible
                     and "Stale snapshot; refresh pending" not in visible)
            if not ready:
                previous, changed = [], time.monotonic()
                return False
            if markers != previous:
                previous, changed = markers, time.monotonic()
            # Observe the preview, not all output: the pending-status spinner
            # can repaint while these fixture rows are already stable.
            return time.monotonic() - changed >= 0.1

        self.wait(settled, "settled fixture preview", timeout)
        return previous

    def visible(self, text, timeout=15):
        self.wait(lambda: text in self.visible_text(), "expected screen state", timeout)


def events(path):
    try:
        body = path.read_text(encoding="utf-8")
    except FileNotFoundError:
        return []
    result = []
    for line in body.splitlines():
        try:
            result.append(json.loads(line))
        except json.JSONDecodeError:
            # A single append may still be in progress when polling.
            continue
    return result


def completed_status(path):
    return [event for event in events(path)
            if event["command"] == "status" and event["phase"] == "end" and event["code"] == 0]


def fixture(root, managed_count, ignored_count, native, git, delay):
    source, destination = root / "source", root / "destination"
    home, config_home = root / "home", root / "config"
    for path in (source / "dot_config", destination / ".config", home,
                 config_home / "chezmoi", root / "tmp"):
        path.mkdir(parents=True)

    for name in ("alpha", "bravo"):
        source_text = "".join(f'line_{line:02} = "STARTUP_{name.upper()}_LINE_{line:02}"\n'
                              for line in range(36))
        (source / "dot_config" / f"{name}.toml").write_text(source_text, encoding="utf-8")
        (destination / ".config" / f"{name}.toml").write_text('current = "fixture"\n', encoding="utf-8")
    for base in (source / "dot_config" / "zbulk", destination / ".config" / "zbulk"):
        base.mkdir()
    for index in range(managed_count - 2):
        filename = f"file_{index:05}.txt"
        (source / "dot_config" / "zbulk" / filename).write_text("source fixture\n", encoding="utf-8")
        (destination / ".config" / "zbulk" / filename).write_text("current fixture\n", encoding="utf-8")

    ignore = ".venv\n.venv/**\nsite\nsite/**\n"
    (source / ".chezmoiignore").write_text(ignore, encoding="utf-8")
    (source / ".gitignore").write_text(ignore, encoding="utf-8")

    env = {key: value for key, value in os.environ.items()
           if not key.upper().startswith(("CHEZMOI_", "LAZYCHEZMOI_RELOAD_", "GIT_"))}
    env.update(
        HOME=str(home), USERPROFILE=str(home), XDG_CONFIG_HOME=str(config_home),
        XDG_DATA_HOME=str(root / "data"), XDG_CACHE_HOME=str(root / "xdg-cache"),
        XDG_STATE_HOME=str(root / "xdg-state"), APPDATA=str(root / "appdata"),
        LOCALAPPDATA=str(root / "localappdata"), TMPDIR=str(root / "tmp"),
        TEMP=str(root / "tmp"), TMP=str(root / "tmp"),
        GIT_CONFIG_GLOBAL=str(root / "empty-gitconfig"), GIT_CONFIG_NOSYSTEM="1",
        TERM="xterm-256color", NO_COLOR="1",
        LAZYCHEZMOI_STARTUP_NATIVE=native,
        LAZYCHEZMOI_STARTUP_LOG=str(root / "native-calls.jsonl"),
        LAZYCHEZMOI_STARTUP_DELAY=f"{delay}s",
    )
    subprocess.run([git, "init", str(source)], check=True, capture_output=True, env=env)
    ignored_roots = [source / ".venv", source / "site", source / ".git" / "startup-fixture"]
    for path in ignored_roots:
        path.mkdir(parents=True, exist_ok=True)
    for index in range(ignored_count):
        (ignored_roots[index % len(ignored_roots)] / f"ignored_{index:05}.txt").write_text("ignored fixture\n", encoding="utf-8")

    editor_log = root / "editor.json"
    editor = root / "editor.py"
    editor.write_text(
        "import json,pathlib,sys,time\n"
        f"pathlib.Path({str(editor_log)!r}).write_text(json.dumps({{'name':pathlib.Path(sys.argv[-1]).name,'time_ns':time.time_ns()}}))\n"
        "print('STARTUP_EDITOR_READY', flush=True)\n",
        encoding="utf-8",
    )
    native_config = config_home / "chezmoi" / "chezmoi.toml"
    native_config.write_text("[edit]\ncommand=" + json.dumps(sys.executable)
                             + "\nargs=[" + json.dumps(str(editor)) + "]\n", encoding="utf-8")
    own_config = root / "lazychezmoi.toml"
    own_config.write_text("auto_fetch=false\ncolor='never'\nmouse=false\n[diff]\nrenderer='builtin'\ntheme='dark'\n", encoding="utf-8")

    proxy_source = root / "native_proxy.go"
    proxy_source.write_text(PROXY_SOURCE, encoding="utf-8")
    proxy = root / ("native-proxy.exe" if os.name == "nt" else "native-proxy")
    # Compilation/setup are deliberately outside measured startup and use the
    # developer's Go build cache, not the synthetic HOME used by the application.
    subprocess.run(["go", "build", "-o", str(proxy), str(proxy_source)], check=True, capture_output=True)
    return source, destination, own_config, proxy, editor_log, env


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=lambda value: str(Path(value).resolve()))
    parser.add_argument("--baseline", action="store_true", help="measure old startup without staged-loading assertions")
    parser.add_argument("--files", type=int, default=2000, help="managed fixture files (minimum 2)")
    parser.add_argument("--ignored", type=int, default=1000, help="ignored fixture files")
    parser.add_argument("--delay", type=float, default=5.0, help="delay per native status invocation in seconds")
    args = parser.parse_args()
    if args.files < 2 or args.ignored < 0 or args.delay < 1:
        parser.error("use --files >= 2, --ignored >= 0, and --delay >= 1")
    native, git, rg = shutil.which("chezmoi"), shutil.which("git"), shutil.which("rg")
    if not native or not git or not rg or not shutil.which("go"):
        raise RuntimeError("chezmoi, git, rg and Go are required")

    with tempfile.TemporaryDirectory(prefix="lazychezmoi-startup-") as directory:
        root = Path(directory)
        source, destination, own_config, proxy, editor_log, env = fixture(
            root, args.files, args.ignored, native, git, args.delay)
        argv = [args.binary, "--chezmoi", str(proxy), "--config", str(own_config),
                "--source", str(source), "--destination", str(destination),
                "--working-tree", str(source), "--chezmoi-cache", str(root / "cache"),
                "--persistent-state", str(root / "state.db"), "--rg", rg, "--auto-fetch=false", "--color=never"]
        # No --chezmoi-config: isolated XDG config discovery must work without
        # the old execute-template query for .chezmoi.configFile.
        log = root / "native-calls.jsonl"
        started, wall_started = time.perf_counter(), time.time_ns()
        terminal = StartupTerminal(argv, env, started)
        terminal.resize(30, 140)
        operation_timings = {}
        startup_calls = {}
        try:
            terminal.wait(lambda: "source_preview" in terminal.milestones,
                          "usable inventory and initial source preview", timeout=args.delay + 20)
            startup_calls = dict(Counter(event["command"] for event in events(log) if event["phase"] == "start"))
            if not args.baseline:
                assert not completed_status(log), "inventory/source preview waited for native status"
                terminal.visible("checking status")

                sent = time.perf_counter()
                terminal.send("j")
                terminal.visible("STARTUP_BRAVO_LINE_00")
                operation_timings["navigate"] = time.perf_counter() - sent
                assert not completed_status(log), "navigation waited for native status"

                sent = time.perf_counter()
                terminal.send("/bravo\r")
                terminal.wait(lambda: "alpha.toml" not in terminal.visible_text()
                              and "STARTUP_BRAVO_LINE_00" in terminal.visible_text(), "filter before status")
                operation_timings["filter"] = time.perf_counter() - sent
                assert not completed_status(log), "filter waited for native status"

                managed_before = sum(event["command"] == "managed" and event["phase"] == "start" for event in events(log))
                sent = time.perf_counter()
                terminal.send("sSTARTUP_BRAVO_LINE_00")
                terminal.visible("1 matches")
                operation_timings["grep"] = time.perf_counter() - sent
                assert not completed_status(log), "content search waited for native status"
                managed_after = sum(event["command"] == "managed" and event["phase"] == "start" for event in events(log))
                assert managed_after == managed_before, "content search repeated cached managed inventory"
                terminal.send("\r1")
                terminal.visible("STARTUP_BRAVO_LINE_00")
                terminal.wait(lambda: "Files" in terminal.visible_text() and "checking status" in terminal.visible_text(), "return to Files")

                sent = time.perf_counter()
                terminal.send("e")
                terminal.wait(editor_log.exists, "editor before status", timeout=args.delay)
                assert json.loads(editor_log.read_text(encoding="utf-8"))["name"] == "bravo.toml", "editor opened the wrong selection"
                assert not completed_status(log), "editor could not launch before native status"
                terminal.visible("Edit source complete", timeout=args.delay)
                operation_timings["edit_return"] = time.perf_counter() - sent

                # Editors may intentionally select the Diff view. Restore Source
                # explicitly, then prove a late status result preserves it.
                for _ in range(4):
                    terminal.drain()
                    if "[Source]" in terminal.visible_text():
                        break
                    terminal.send("v")
                    time.sleep(0.06)
                terminal.visible("[Source]")
                terminal.visible("STARTUP_BRAVO_LINE_00")
                terminal.send("ljj")  # Explicit preview focus, independent of prior pane focus.
                markers_before = terminal.settled_markers(
                    r"STARTUP_BRAVO_LINE_\d+", first="STARTUP_BRAVO_LINE_02")
                assert not completed_status(log), "preview scroll snapshot waited for native status"
                terminal.visible("status ready", timeout=args.delay + 15)
                terminal.milestones["status_ready"] = time.perf_counter() - started
                assert "[Source]" in terminal.visible_text(), "automatic status changed the preview mode"
                markers_after = terminal.settled_markers(r"STARTUP_BRAVO_LINE_\d+")
                assert markers_after == markers_before, "automatic status reset selection or preview scroll"
                assert "alpha.toml" not in terminal.visible_text(), "automatic status cleared the filter"
            else:
                terminal.wait(lambda: bool(completed_status(log)), "baseline status completion", timeout=args.delay + 15)

            completed = completed_status(log)
            assert completed, "no successful native status completion observed"
            terminal.milestones["native_status_complete"] = (completed[-1]["time_ns"] - wall_started) / 1e9
            calls = Counter(event["command"] for event in events(log) if event["phase"] == "start")
            if not args.baseline:
                assert calls["execute-template"] == 0, "startup still renders a template to discover context"
            terminal.send("q")
            terminal.finish()
        except AssertionError as error:
            observed = events(log)
            started_calls = Counter(event["command"] for event in observed if event["phase"] == "start")
            ended_calls = Counter(event["command"] for event in observed if event["phase"] == "end")
            screen_counts = {word: terminal.output.lower().count(word) for word in
                             ("vim", "continue", "press", "password", "not found", "edit source failed", "startup_editor_ready")}
            raise AssertionError(str(error).splitlines()[0] + "; " + json.dumps({
                "native_calls": dict(started_calls), "completed_calls": dict(ended_calls),
                "screen_counts": screen_counts,
                "seconds": {key: round(value, 4) for key, value in terminal.milestones.items()},
            }, sort_keys=True)) from None
        finally:
            terminal.close()

        print(json.dumps({
            "mode": "baseline" if args.baseline else "staged",
            "managed_files": args.files, "ignored_files": args.ignored,
            "status_delay_seconds": args.delay,
            "seconds": {key: round(value, 4) for key, value in terminal.milestones.items()},
            "interaction_seconds": {key: round(value, 4) for key, value in operation_timings.items()},
            "startup_native_calls": dict(sorted(startup_calls.items())),
            "native_calls": dict(sorted(calls.items())),
            "terminal_queries": dict(sorted(terminal.query_counts.items())),
        }, sort_keys=True))


if __name__ == "__main__":
    try:
        main()
    except (AssertionError, RuntimeError, subprocess.CalledProcessError) as error:
        # Keep failures useful without dumping terminal source/config bodies.
        print(json.dumps({"error": str(error).splitlines()[0]}), file=sys.stderr)
        raise SystemExit(1) from None
