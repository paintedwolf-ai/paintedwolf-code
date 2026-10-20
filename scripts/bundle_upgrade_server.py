"""Serve only the pinned updater routes over runner-local TLS."""
from __future__ import annotations

from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import ssl
import threading


class FixtureServer(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, address, routes: dict[str, Path], log: Path):
        super().__init__(address, Handler)
        self.routes = routes
        self.log = log
        self.log_lock = threading.Lock()
        self.requests = []


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        path = self.server.routes.get(self.path)
        status = 200 if path is not None else 404
        with self.server.log_lock:
            row = {"path": self.path, "status": status}
            self.server.requests.append(row)
            with self.server.log.open("a") as log:
                log.write(json.dumps(row) + "\n")
        if path is None:
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header("Content-Length", str(path.stat().st_size))
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Type", "application/octet-stream")
        self.end_headers()
        try:
            with path.open("rb") as source:
                while block := source.read(1024 * 1024):
                    self.wfile.write(block)
        except (BrokenPipeError, ConnectionResetError):
            pass


def routes_for(root: Path, plan: dict) -> dict[str, Path]:
    routes = {}
    for relative in plan["archives"].values():
        from bundle_upgrade_prepare import archive_path
        path = archive_path(relative)
        routes["/" + path] = root / path
    for pointer in (root / "offers").glob("*/key-*/latest.json"):
        channel, key = pointer.parent.parent.name, pointer.parent.name
        route = f"/updates/{channel}/{key}/latest.json"
        routes[route] = pointer
        routes[route + ".sig"] = pointer.with_suffix(".json.sig")
    return routes


@contextmanager
def serving(routes: dict[str, Path], certificate: Path, key: Path, log: Path, port: int = 443):
    server = FixtureServer(("127.0.0.1", port), routes, log)
    try:
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(certificate, key)
        server.socket = context.wrap_socket(server.socket, server_side=True)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            yield server
        finally:
            server.shutdown()
            thread.join(timeout=10)
    finally:
        server.server_close()
