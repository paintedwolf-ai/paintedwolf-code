"""Observe and stop only processes launched from the rehearsal installation."""
from __future__ import annotations

import json
import os
from pathlib import Path
import signal
import subprocess
import time
import urllib.request


def wait_for(description, observe, seconds=180):
    deadline = time.monotonic() + seconds
    while True:
        value = observe()
        if value:
            return value
        if time.monotonic() >= deadline:
            raise TimeoutError(description)
        time.sleep(0.25)


def read_json(path: Path):
    try:
        return json.loads(path.read_text())
    except (FileNotFoundError, json.JSONDecodeError):
        return None


def process_identity(pid: int) -> str | None:
    result = subprocess.run(["ps", "-p", str(pid), "-o", "lstart=,comm="], capture_output=True, text=True, timeout=5)
    return result.stdout.strip() if result.returncode == 0 else None


class Installation:
    def __init__(self, app: Path, config: Path):
        import plistlib
        self.app, self.config = app, config
        info = plistlib.loads((app / "Contents/Info.plist").read_bytes())
        self.executable = app / "Contents/MacOS" / info["CFBundleExecutable"]
        self.owned = {}

    def launch(self):
        subprocess.run(["/usr/bin/open", "-n", "-j", "--env", "LYCAON_LLM_MOCK=1", str(self.app)], check=True, timeout=30)

    def capture_owned(self):
        helpers = self.config.glob("updates/installations/*/staging/*/update-helper")
        commands = {str(self.executable): 1, **{str(path): 0 for path in helpers},
                    str(self.app / "Contents/MacOS/pw"): 2,
                    str(self.app / "Contents/Helpers/Painted Wolf Code engine.app/Contents/MacOS/pw"): 2}
        result = subprocess.run(["ps", "-axo", "pid=,ppid=,comm="], check=True, capture_output=True, text=True, timeout=5)
        rows = [row.strip().split(None, 2) for row in result.stdout.splitlines()]
        rows = [(int(row[0]), int(row[1]), row[2]) for row in rows if len(row) == 3 and row[0].isdigit() and row[1].isdigit()]
        for pid, _, executable in rows:
            if executable in commands:
                identity = process_identity(pid)
                if identity:
                    self.owned[pid] = (identity, commands[executable])
        changed = True
        while changed:
            changed = False
            for pid, parent, _ in rows:
                if pid not in self.owned and parent in self.owned and process_identity(parent) == self.owned[parent][0]:
                    identity = process_identity(pid)
                    if identity:
                        self.owned[pid] = (identity, 3)
                        changed = True

    def observe(self, version: str):
        self.capture_owned()
        daemon = read_json(self.config / "daemon.json")
        if not daemon or type(daemon.get("pid")) is not int or type(daemon.get("port")) is not int:
            return None
        pid, port = daemon["pid"], daemon["port"]
        if pid <= 1 or not 1 <= port <= 65535:
            raise ValueError("invalid rehearsal daemon identity")
        engine = process_identity(pid)
        if self.owned.get(pid) != (engine, 2):
            return None
        parent = subprocess.run(["ps", "-p", str(pid), "-o", "ppid="], check=True, capture_output=True, text=True, timeout=5)
        app_pid = int(parent.stdout.strip())
        app_identity = process_identity(app_pid)
        if self.owned.get(app_pid) != (app_identity, 1):
            return None
        self.owned[pid] = (engine, 2)
        self.owned[app_pid] = (app_identity, 1)
        try:
            client = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            with client.open(f"http://127.0.0.1:{port}/health", timeout=2) as response:
                health = json.load(response)
        except (OSError, ValueError):
            return None
        if health.get("version") != version or health.get("status") != "ok":
            return None
        return {"health": health, "engine_pid": pid, "app_pid": app_pid, "port": port}

    def healthy(self, version: str):
        return wait_for(f"installed app {version} never reached healthy startup", lambda: self.observe(version))

    def stop(self, crash_app=False):
        self.capture_owned()
        errors = []
        # Retire helpers and app supervision before stopping captured engines.
        for pid, (identity, kind) in sorted(self.owned.items(), key=lambda item: item[1][1]):
            if process_identity(pid) != identity:
                continue
            try:
                os.kill(pid, signal.SIGKILL if crash_app and kind == 1 else signal.SIGTERM)
            except ProcessLookupError:
                continue
            deadline = time.monotonic() + 10
            while process_identity(pid) == identity and time.monotonic() < deadline:
                time.sleep(0.1)
            if process_identity(pid) == identity:
                os.kill(pid, signal.SIGKILL)
                try:
                    wait_for(f"captured process {pid} did not exit", lambda: process_identity(pid) != identity, 5)
                except TimeoutError as error:
                    errors.append(str(error))
        if errors:
            raise ValueError("; ".join(errors))
        self.owned.clear()
