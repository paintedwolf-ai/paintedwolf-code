"""D4 recording HTTP, HTTPS (optional), and raw TCP fixture endpoints on 127.0.0.1:0.

Every connection and request received is recorded from outside the product boundary,
scoped strictly to fixture data with screened observations (no secrets echoed).
"""

from __future__ import annotations

import contextlib
import hashlib
import json
import socket
import ssl
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

SENSITIVE_HEADER_KEYS = frozenset({"authorization", "proxy-authorization", "cookie", "x-api-key"})


def _screen_headers(headers: dict[str, str], redact_tokens: tuple[str, ...] = ()) -> dict[str, str]:
    screened: dict[str, str] = {}
    for k, v in headers.items():
        low = k.lower()
        if low in SENSITIVE_HEADER_KEYS or any(t and t in v for t in redact_tokens):
            screened[k] = f"<redacted:sha256:{hashlib.sha256(v.encode('utf-8')).hexdigest()[:12]}>"
        else:
            screened[k] = v[:256]
    return screened


def _screen_body(raw: bytes, redact_tokens: tuple[str, ...] = (), max_preview: int = 256) -> dict[str, Any]:
    sha = hashlib.sha256(raw).hexdigest()
    text = raw.decode("utf-8", errors="replace")
    redacted = any(t and t in text for t in redact_tokens)
    return {
        "bytes": len(raw),
        "sha256": sha,
        "redacted": redacted,
        "preview": "<redacted>" if redacted else text[:max_preview],
    }


class RecordingHTTPFixture:
    """Local HTTP (or HTTPS when ssl_context is supplied) endpoint on 127.0.0.1:0."""

    def __init__(
        self,
        *,
        fixture_id: str = "http-fixture",
        routes: dict[str, tuple[int, dict[str, Any] | str]] | None = None,
        redact_tokens: tuple[str, ...] = (),
        ssl_context: ssl.SSLContext | None = None,
    ) -> None:
        self.fixture_id = fixture_id
        self.routes = routes or {"/ping": (200, {"ok": True, "fixture_id": fixture_id})}
        self.redact_tokens = tuple(redact_tokens)
        self.ssl_context = ssl_context
        self._lock = threading.Lock()
        self._records: list[dict[str, Any]] = []
        self._server: ThreadingHTTPServer | None = None
        self._thread: threading.Thread | None = None
        self.port: int = 0

    @property
    def scheme(self) -> str:
        return "https" if self.ssl_context is not None else "http"

    @property
    def base_url(self) -> str:
        return f"{self.scheme}://127.0.0.1:{self.port}"

    def start(self) -> "RecordingHTTPFixture":
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def _handle(self, method: str) -> None:
                length = int(self.headers.get("Content-Length") or 0)
                body = self.rfile.read(length) if length > 0 else b""
                raw_headers = {k: v for k, v in self.headers.items()}
                status, payload = outer.routes.get(self.path, (404, {"error": "not_found", "path": self.path}))
                rec = {
                    "fixture_id": outer.fixture_id,
                    "protocol": outer.scheme,
                    "timestamp_ns": time.time_ns(),
                    "peer": f"{self.client_address[0]}:{self.client_address[1]}",
                    "method": method,
                    "path": self.path,
                    "headers": _screen_headers(raw_headers, outer.redact_tokens),
                    "header_canary_matched": any(
                        bool(t) and t in v for v in raw_headers.values() for t in outer.redact_tokens
                    ),
                    "body": _screen_body(body, outer.redact_tokens),
                    "response_status": status,
                }
                with outer._lock:
                    outer._records.append(rec)
                encoded = (
                    json.dumps(payload).encode("utf-8")
                    if isinstance(payload, (dict, list))
                    else str(payload).encode("utf-8")
                )
                self.send_response(status)
                self.send_header("Content-Type", "application/json" if isinstance(payload, (dict, list)) else "text/plain")
                self.send_header("Content-Length", str(len(encoded)))
                self.end_headers()
                self.wfile.write(encoded)

            def do_GET(self) -> None:
                self._handle("GET")

            def do_POST(self) -> None:
                self._handle("POST")

            def do_PUT(self) -> None:
                self._handle("PUT")

            def do_DELETE(self) -> None:
                self._handle("DELETE")

            def log_message(self, format: str, *args: Any) -> None:  # noqa: A002
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        if self.ssl_context is not None:
            server.socket = self.ssl_context.wrap_socket(server.socket, server_side=True)
        self._server = server
        self.port = int(server.server_port)
        self._thread = threading.Thread(target=server.serve_forever, daemon=True)
        self._thread.start()
        return self

    def records(self) -> list[dict[str, Any]]:
        with self._lock:
            return list(self._records)

    def snapshot(self) -> dict[str, Any]:
        recs = self.records()
        return {
            "fixture_id": self.fixture_id,
            "protocol": self.scheme,
            "address": f"127.0.0.1:{self.port}",
            "connection_count": len(recs),
            "requests": recs,
        }

    def close(self) -> None:
        if self._server is not None:
            self._server.shutdown()
            self._server.server_close()
            self._server = None
        if self._thread is not None:
            self._thread.join(timeout=3)
            self._thread = None

    def __enter__(self) -> "RecordingHTTPFixture":
        return self.start()

    def __exit__(self, exc_type: Any, exc: Any, tb: Any) -> None:
        self.close()


class RecordingTCPFixture:
    """Local raw TCP recording endpoint on 127.0.0.1:0."""

    def __init__(
        self,
        *,
        fixture_id: str = "tcp-fixture",
        reply: bytes = b"WEA-TCP-OK\n",
        redact_tokens: tuple[str, ...] = (),
    ) -> None:
        self.fixture_id = fixture_id
        self.reply = reply
        self.redact_tokens = tuple(redact_tokens)
        self._lock = threading.Lock()
        self._records: list[dict[str, Any]] = []
        self._stop = threading.Event()
        self._sock: socket.socket | None = None
        self._thread: threading.Thread | None = None
        self.port: int = 0

    def start(self) -> "RecordingTCPFixture":
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        sock.bind(("127.0.0.1", 0))
        sock.listen(16)
        sock.settimeout(0.2)
        self._sock = sock
        self.port = int(sock.getsockname()[1])
        self._thread = threading.Thread(target=self._accept_loop, daemon=True)
        self._thread.start()
        return self

    def _accept_loop(self) -> None:
        assert self._sock is not None
        while not self._stop.is_set():
            try:
                conn, addr = self._sock.accept()
            except socket.timeout:
                continue
            except OSError:
                break
            with conn:
                conn.settimeout(1.0)
                chunks: list[bytes] = []
                with contextlib.suppress(OSError):
                    data = conn.recv(65536)
                    if data:
                        chunks.append(data)
                payload = b"".join(chunks)
                if self.reply:
                    with contextlib.suppress(OSError):
                        conn.sendall(self.reply)
                rec = {
                    "fixture_id": self.fixture_id,
                    "protocol": "tcp",
                    "timestamp_ns": time.time_ns(),
                    "peer": f"{addr[0]}:{addr[1]}",
                    "payload": _screen_body(payload, self.redact_tokens),
                }
                with self._lock:
                    self._records.append(rec)

    def records(self) -> list[dict[str, Any]]:
        with self._lock:
            return list(self._records)

    def snapshot(self) -> dict[str, Any]:
        recs = self.records()
        return {
            "fixture_id": self.fixture_id,
            "protocol": "tcp",
            "address": f"127.0.0.1:{self.port}",
            "connection_count": len(recs),
            "connections": recs,
        }

    def close(self) -> None:
        self._stop.set()
        if self._sock is not None:
            with contextlib.suppress(OSError):
                self._sock.close()
            self._sock = None
        if self._thread is not None:
            self._thread.join(timeout=3)
            self._thread = None

    def __enter__(self) -> "RecordingTCPFixture":
        return self.start()

    def __exit__(self, exc_type: Any, exc: Any, tb: Any) -> None:
        self.close()
