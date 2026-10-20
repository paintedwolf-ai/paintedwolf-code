"""Own one final installed desktop process and its actual engine connection."""
from __future__ import annotations

import json
import os
from pathlib import Path
import stat
import subprocess
import threading
import time
import urllib.parse

from .app_api import EngineAPI
from .products import require_owned_guest
from .records import CaseUnavailable
from ..installed_environment import graphical_session
from ..installed_runtime import file_identity
from scripts.harness.supervise import stop_stack

LIMIT = 8 << 20


def process_identity(pid: int) -> dict:
    row = Path(f"/proc/{pid}/stat").read_text().rpartition(") ")[2].split()
    if len(row) < 20:
        raise ValueError("installed process identity is unavailable")
    return {"pid": pid, "parent_pid": int(row[1]), "start_time_ticks": int(row[19])}


def private_json(path: Path) -> dict:
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(descriptor, "rb") as stream:
        info = os.fstat(stream.fileno())
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid()
                or stat.S_IMODE(info.st_mode) & 0o077 or info.st_size > 4096):
            raise ValueError("installed process record is not a private owned regular file")
        raw = stream.read(4097)
        if len(raw) != info.st_size:
            raise ValueError("installed process record changed while reading")
        return json.loads(raw)


def session_environment(session: dict) -> dict:
    home, runtime = Path.home(), Path(f"/run/user/{os.getuid()}")
    info = runtime.stat()
    bus = runtime / "bus"
    if info.st_uid != os.getuid() or not stat.S_ISSOCK(bus.stat().st_mode):
        raise CaseUnavailable("native graphical session bus is unavailable")
    result = {"HOME": str(home), "USER": home.name, "LOGNAME": home.name,
              "PATH": "/run/current-system/sw/bin:/usr/bin:/bin", "LC_ALL": "C.UTF-8",
              "XDG_RUNTIME_DIR": str(runtime), "DBUS_SESSION_BUS_ADDRESS": "unix:path=" + str(bus),
              "XDG_SESSION_ID": session["id"], "XDG_SESSION_TYPE": session["Type"],
              "XDG_CURRENT_DESKTOP": session["Desktop"]}
    if session["Type"] == "wayland":
        sockets = [path for path in runtime.glob("wayland-*") if stat.S_ISSOCK(path.lstat().st_mode)
                   and path.lstat().st_uid == os.getuid()]
        if len(sockets) != 1:
            raise CaseUnavailable("native Wayland display socket is not uniquely selected")
        result["WAYLAND_DISPLAY"] = sockets[0].name
    else:
        if not session.get("Display"):
            raise CaseUnavailable("native X11 display is not reported by its session owner")
        result["DISPLAY"] = session["Display"]
        authority = home / ".Xauthority"
        if authority.is_file():
            result["XAUTHORITY"] = str(authority)
    return result


class InstalledApp:
    def __init__(self, binding: dict, *, arguments: list[str] | None = None, timeout: float = 60):
        self.binding, self.arguments, self.timeout = binding, arguments or [], timeout
        if not 0 < timeout <= 300:
            raise ValueError("installed launcher readiness timeout exceeds its bound")
        for value in self.arguments:
            if not isinstance(value, str) or "\0" in value:
                raise ValueError("installed launcher arguments must be literal fixture paths")
            parsed = urllib.parse.urlsplit(value)
            path = Path(urllib.parse.unquote(parsed.path)) if parsed.scheme == "file" and not parsed.netloc else Path(value)
            if not path.is_absolute() or not path.exists() or parsed.scheme not in {"", "file"}:
                raise ValueError("installed launcher argument is not an existing absolute fixture path")
        self.process = None
        self.api = None
        self.observations = {"artifact": binding["artifact"], "source_commit": binding["source_commit"]}
        self._streams = {"app-stdout": bytearray(), "app-stderr": bytearray()}
        self._readers = []
        self._overflow = threading.Event()

    def __enter__(self):
        return self.start()

    def __exit__(self, kind, value, traceback):
        self.stop()

    def start(self):
        require_owned_guest(self.binding)
        session = graphical_session()
        environment = session_environment(session)
        config = Path.home() / ".config/paintedwolf"
        daemon = config / "daemon.json"
        if daemon.exists():
            previous = private_json(daemon)
            pid = previous.get("pid")
            if type(pid) is int and pid > 1 and Path(f"/proc/{pid}").exists():
                raise CaseUnavailable("guest already has a running engine; preserving its owner")
        launcher = dict(self.binding["launcher"])
        path = Path(launcher.pop("path"))
        current = file_identity(path)
        current.pop("header")
        if current != launcher:
            raise ValueError("installed application launcher changed since final artifact binding")
        self.process = subprocess.Popen([str(path), *self.arguments], env=environment,
            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
        self.observations.update({"launcher": self.binding["launcher"], "session": session,
                                  "launch_identity": process_identity(self.process.pid)})

        def capture(name, stream):
            with stream:
                while block := stream.read(64 << 10):
                    if len(self._streams[name]) + len(block) > LIMIT:
                        self._overflow.set()
                        return
                    self._streams[name].extend(block)

        self._readers = [threading.Thread(target=capture, args=(name, getattr(self.process, stream)), daemon=True)
                         for name, stream in (("app-stdout", "stdout"), ("app-stderr", "stderr"))]
        for reader in self._readers:
            reader.start()
        deadline = time.monotonic() + self.timeout
        try:
            while time.monotonic() < deadline and self.process.poll() is None and not self._overflow.is_set():
                if daemon.exists():
                    record = private_json(daemon)
                    if self._bind_engine(record, config):
                        return self
                time.sleep(0.1)
            self.observations["exit_code"] = self.process.poll()
            raise CaseUnavailable("installed application did not reach an owned engine API; onboarding or a native interaction driver may be required")
        except BaseException as error:
            self.stop()
            if isinstance(error, CaseUnavailable):
                error.observations, error.streams = self.observations, self.streams()
            raise

    def _bind_engine(self, record: dict, config: Path) -> bool:
        pid, port = record.get("pid"), record.get("port")
        if type(pid) is not int or pid <= 1 or record.get("host") not in {"127.0.0.1", "localhost"}:
            raise ValueError("installed engine record names a foreign endpoint")
        try:
            engine = process_identity(pid)
        except FileNotFoundError:
            return False
        if engine["parent_pid"] != self.process.pid:
            raise ValueError("installed engine is not the captured application's child")
        expected = self.binding["images"][str(Path(self.binding["install_root"]) / "bin/pw")]
        image = Path(f"/proc/{pid}/exe").stat()
        if (image.st_dev, image.st_ino, image.st_size) != (expected["device"], expected["inode"], expected["bytes"]):
            raise ValueError("running engine differs from the selected final installed image")
        token_path = config / "api.token"
        descriptor = os.open(token_path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(descriptor, "rb") as stream:
            info = os.fstat(stream.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) & 0o077 or info.st_size > 4096:
                raise ValueError("installed API token is not a private owned file")
            token = stream.read(4097).decode().strip()
        api = EngineAPI(port, token)
        try:
            health = api.request("GET", "/health")
        except (OSError, ValueError):
            return False
        if health["status"] != 200:
            return False
        if process_identity(pid) != engine:
            raise ValueError("installed engine changed during readiness capture")
        self.api = api
        self.observations.update({"engine_identity": engine, "engine_port": port, "health": health})
        return True

    def request(self, method: str, path: str, payload=None) -> dict:
        if self.api is None:
            raise CaseUnavailable("installed application has no captured ready engine")
        actual = process_identity(self.observations["engine_identity"]["pid"])
        if actual != self.observations["engine_identity"]:
            raise ValueError("installed API process identity changed")
        return self.api.request(method, path, payload)

    def stop(self):
        if self.process is None:
            return
        stop_stack(self.process)
        for reader in self._readers:
            reader.join(timeout=3)
        self.observations.update({"exit_code": self.process.poll(), "output_limit": self._overflow.is_set(),
                                  "capture_complete": not any(reader.is_alive() for reader in self._readers)})
        self.api = None

    def streams(self) -> dict[str, bytes]:
        return {name: bytes(raw) for name, raw in self._streams.items()}
