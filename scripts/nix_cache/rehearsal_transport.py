"""Exercise real signed cache delivery using an owned loopback HTTP fixture."""
from __future__ import annotations

from contextlib import contextmanager
import datetime as dt
import fcntl
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import os
from pathlib import Path
import tempfile
import threading

from .admission import inventory
from .publication import publish, record_name, withdraw
from .r2 import R2Transport, relative_name
from .retention import collect


class FixtureStore:
    def __init__(self, root: Path, state: dict):
        self.root, self.state, self.headers = root, state, {}

    @contextmanager
    def serialized(self, operation):
        with (self.root.parent / "lock").open("a+b") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            try:
                yield self
            finally:
                fcntl.flock(lock, fcntl.LOCK_UN)

    def read(self, name):
        path = self.root / relative_name(name)
        return path.read_bytes() if path.exists() else None

    def put_bytes(self, name, data, content_type, cache_control):
        path = self.root / relative_name(name)
        path.parent.mkdir(parents=True, exist_ok=True)
        if path.exists():
            if path.read_bytes() != data:
                raise ValueError("fixture immutable cache object differs")
        else:
            with path.open("xb") as destination:
                destination.write(data)
        self.headers[name] = cache_control

    def put(self, name, path, record):
        self.put_bytes(name, path.read_bytes(), record["content_type"], record["cache_control"])

    def replace(self, name, path, record, previous):
        if self.read(name) != previous:
            raise ValueError("fixture rotation metadata changed")
        (self.root / relative_name(name)).write_bytes(path.read_bytes())
        self.headers[name] = record["cache_control"]

    def verify_public(self, name, record):
        # The production verifier is used against the owned fixture, with no R2 backend.
        R2Transport.verify_public(self, name, record)

    def listing(self):
        return [{"name": path.relative_to(self.root).as_posix(),
                 "modified": dt.datetime.fromtimestamp(path.stat().st_mtime, dt.timezone.utc)}
                for path in self.root.rglob("*") if path.is_file()]

    def delete(self, row):
        (self.root / row["name"]).unlink()


def run(cache: Path, report: dict, rotated: Path, keys: list[str]) -> list[str]:
    if report["request"]["mode"] != "rehearsal":
        raise ValueError("local HTTP cache fixture refuses production requests")
    with tempfile.TemporaryDirectory(prefix="paintedwolf-cache-http-") as directory:
        root = Path(directory) / "objects"
        root.mkdir()
        metadata = next(row["name"] for row in report["objects"] if row["phase"] == "narinfo")
        state = {"missing": metadata}
        backend = FixtureStore(root, state)
        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                if "?" in self.path:
                    raise ValueError("fixture must verify the ordinary cache URL")
                name = relative_name(self.path.removeprefix("/"))
                data = backend.read(name)
                self.send_response(404 if data is None or state.get("missing") == name else 200)
                self.send_header("Cache-Control", backend.headers.get(name, "no-store"))
                self.end_headers()
                self.wfile.write(data or b"")

            def log_message(self, *args):
                pass
        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        backend.public = f"http://127.0.0.1:{server.server_port}"
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            arguments = {"authorize": lambda request, checked: request == report["request"], "backend_factory": lambda request: backend}
            try:
                publish(cache, report, **arguments)
            except ValueError:
                pass
            else:
                raise ValueError("cached negative response did not refuse cache publication")
            if backend.read(record_name(report["request"])) is not None:
                raise ValueError("interrupted cache publication advertised availability")
            state.clear()
            publish(cache, report, **arguments)
            publish(cache, report, **arguments)
            rotation = {**report, "public_keys": keys, "objects": inventory(rotated, report["closure"], keys)}
            publish(rotated, rotation, **arguments)
            withdraw(report["request"], authorize=lambda request: True, backend_factory=arguments["backend_factory"])
            backend.put_bytes("nar/orphan.nar.xz", b"never advertised", "application/octet-stream", "no-store")
            old = dt.datetime.now(dt.timezone.utc) - dt.timedelta(days=7)
            os.utime(root / "nar/orphan.nar.xz", (old.timestamp(), old.timestamp()))
            removed = collect(report["request"], old + dt.timedelta(days=1), authorize=lambda request, cutoff: True,
                              backend_factory=arguments["backend_factory"])
            if removed != ["nar/orphan.nar.xz"] or any(backend.read(row["name"]) is None for row in report["objects"]):
                raise ValueError("cache retention removed published or retained closure objects")
        finally:
            server.shutdown()
            server.server_close()
            thread.join()
    return ["ordinary-http", "negative-cache-refusal", "interrupted-retry", "signature-addition-rotation", "withdrawal-retention", "unreferenced-debris-gc"]
