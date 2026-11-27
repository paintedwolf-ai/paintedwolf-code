"""Project-scoped live /v1/events SSE subscriber with bounded readiness, drain, and teardown."""

from __future__ import annotations

import contextlib
import http.client
import json
from pathlib import Path
import socket
import threading
import time
from typing import Callable, Protocol
import urllib.parse


class SSESubscription(Protocol):
    """Protocol for live or stubbed project-scoped /v1/events SSE subscriptions."""

    def start(self) -> None: ...
    def wait_ready(self, max_wait_s: float = 10.0) -> bool: ...
    def wait_for_frames(
        self,
        predicate: Callable[[list[dict[str, object]]], bool],
        max_wait_s: float = 10.0,
    ) -> tuple[list[dict[str, object]], bool]: ...
    def frames(self) -> list[dict[str, object]]: ...
    def close(self, max_wait_s: float = 3.0) -> None: ...


class LiveSSESubscriber:
    """Real project-scoped `/v1/events?project_id=<id>` SSE subscriber with bounded waits."""

    def __init__(
        self,
        base_url: str,
        token: str,
        *,
        project_id: str,
        client_id: str = "cfs-driver",
        out_jsonl: Path | None = None,
        connect_timeout_s: float = 10.0,
    ) -> None:
        if not project_id:
            raise ValueError("project_id is required for project-scoped /v1/events subscription")
        self._parsed = urllib.parse.urlsplit(base_url.rstrip("/"))
        qs = urllib.parse.urlencode({"project_id": project_id, "client_id": client_id})
        base_path = self._parsed.path.rstrip("/")
        self._request_target = f"{base_path}/v1/events?{qs}"
        self._token = token
        self._project_id = project_id
        self._out_jsonl = Path(out_jsonl) if out_jsonl else None
        self._connect_timeout_s = connect_timeout_s

        self._ready = threading.Event()
        self._stop = threading.Event()
        self._cond = threading.Condition()
        self._frames: list[dict[str, object]] = []
        self._conn: http.client.HTTPConnection | None = None
        self._error: Exception | None = None
        self._thread = threading.Thread(target=self._run, daemon=True)

    @property
    def request_target(self) -> str:
        return self._request_target

    @property
    def error(self) -> Exception | None:
        return self._error

    def start(self) -> None:
        self._thread.start()

    def wait_ready(self, max_wait_s: float = 10.0) -> bool:
        """Wait up to max_wait_s for HTTP 200 SSE stream open before executing writes."""
        if not self._ready.wait(timeout=max_wait_s):
            return False
        return self._error is None

    def frames(self) -> list[dict[str, object]]:
        with self._cond:
            return list(self._frames)

    def wait_for_frames(
        self,
        predicate: Callable[[list[dict[str, object]]], bool],
        max_wait_s: float = 10.0,
    ) -> tuple[list[dict[str, object]], bool]:
        """Wait reactively up to max_wait_s until predicate(frames) holds or stream closes."""
        deadline = time.monotonic() + max_wait_s
        with self._cond:
            while not predicate(self._frames):
                if self._stop.is_set():
                    return list(self._frames), False
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    return list(self._frames), False
                self._cond.wait(timeout=remaining)
            return list(self._frames), True

    def _open_connection(self) -> http.client.HTTPConnection:
        host = self._parsed.hostname or "127.0.0.1"
        port = self._parsed.port
        if self._parsed.scheme == "https":
            return http.client.HTTPSConnection(host, port, timeout=self._connect_timeout_s)
        return http.client.HTTPConnection(host, port, timeout=self._connect_timeout_s)

    def _run(self) -> None:
        sink = self._out_jsonl.open("w", encoding="utf-8") if self._out_jsonl else None
        conn: http.client.HTTPConnection | None = None
        try:
            conn = self._open_connection()
            self._conn = conn
            conn.request(
                "GET",
                self._request_target,
                headers={
                    "Authorization": f"Bearer {self._token}",
                    "Accept": "text/event-stream",
                },
            )
            resp = conn.getresponse()
            if resp.status != 200:
                raise RuntimeError(f"SSE /v1/events returned HTTP {resp.status}")
            # Remove read timeout once streaming so idle periods rely on explicit close/deadline.
            if conn.sock is not None:
                conn.sock.settimeout(None)
            self._ready.set()

            while not self._stop.is_set():
                raw = resp.readline()
                if not raw:
                    break
                line = raw.decode("utf-8", errors="replace").rstrip("\r\n")
                if not line.startswith("data:"):
                    continue
                payload = line[5:].strip()
                if not payload:
                    continue
                if sink is not None:
                    sink.write(payload + "\n")
                    sink.flush()
                try:
                    parsed = json.loads(payload)
                except ValueError:
                    continue
                if isinstance(parsed, dict):
                    with self._cond:
                        self._frames.append(parsed)
                        self._cond.notify_all()
        except Exception as exc:
            if not self._stop.is_set():
                self._error = exc
        finally:
            self._ready.set()
            self._stop.set()
            with self._cond:
                self._cond.notify_all()
            if conn is not None:
                with contextlib.suppress(OSError):
                    conn.close()
            if sink is not None:
                with contextlib.suppress(OSError):
                    sink.close()

    def close(self, max_wait_s: float = 3.0) -> None:
        """Interrupt ONLY this subscriber's owned socket and join the reader thread within max_wait_s."""
        self._stop.set()
        conn = self._conn
        if conn is not None:
            sock = conn.sock
            if sock is not None:
                with contextlib.suppress(OSError):
                    sock.shutdown(socket.SHUT_RDWR)
                with contextlib.suppress(OSError):
                    sock.close()
            with contextlib.suppress(OSError):
                conn.close()
        with self._cond:
            self._cond.notify_all()
        if self._thread.is_alive():
            self._thread.join(timeout=max_wait_s)
