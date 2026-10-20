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
    result = subprocess.run(["ps", "-p", str(pid), "-o", "lstart=,command="], capture_output=True, text=True, timeout=5)
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

    def observe(self, version: str):
        daemon = read_json(self.config / "daemon.json")
        if not daemon or type(daemon.get("pid")) is not int or type(daemon.get("port")) is not int:
            return None
        pid, port = daemon["pid"], daemon["port"]
        if pid <= 1 or not 1 <= port <= 65535:
            raise ValueError("invalid rehearsal daemon identity")
        engine = process_identity(pid)
        if engine is None or str(self.app) + "/Contents/" not in engine:
            return None
        parent = subprocess.run(["ps", "-p", str(pid), "-o", "ppid="], check=True, capture_output=True, text=True, timeout=5)
        app_pid = int(parent.stdout.strip())
        app_identity = process_identity(app_pid)
        if not app_identity or str(self.executable) not in app_identity:
            return None
        self.owned[pid] = engine
        self.owned[app_pid] = app_identity
        try:
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/health", timeout=2) as response:
                health = json.load(response)
        except (OSError, ValueError):
            return None
        if health.get("version") != version or health.get("status") != "ok":
            return None
        return {"health": health, "engine_pid": pid, "app_pid": app_pid, "port": port}

    def healthy(self, version: str):
        return wait_for(f"installed app {version} never reached healthy startup", lambda: self.observe(version))

    def stop(self):
        errors = []
        # Stop the app before its captured engine so supervision cannot restart it.
        for pid, identity in reversed(list(self.owned.items())):
            if process_identity(pid) != identity:
                continue
            os.kill(pid, signal.SIGTERM)
            deadline = time.monotonic() + 10
            while process_identity(pid) == identity and time.monotonic() < deadline:
                time.sleep(0.1)
            if process_identity(pid) == identity:
                os.kill(pid, signal.SIGKILL)
                try:
                    wait_for(f"captured process {pid} did not exit", lambda: process_identity(pid) != identity, 5)
                except TimeoutError as error:
                    errors.append(str(error))
        self.owned.clear()
        if errors:
            raise ValueError("; ".join(errors))
