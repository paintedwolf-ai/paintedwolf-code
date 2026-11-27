"""Live /v1/events SSE subscriber with readiness barrier and reactive frame drain."""

from __future__ import annotations

import contextlib
import json
from pathlib import Path
import threading
from typing import Callable, Protocol
import urllib.request


class SSESubscription(Protocol):
    """Protocol for live or stubbed /v1/events SSE subscriptions."""

    def start(self) -> None: ...
    def wait_ready(self) -> bool: ...
    def wait_for_frames(self, predicate: Callable[[list[dict[str, object]]], bool]) -> list[dict[str, object]]: ...
    def frames(self) -> list[dict[str, object]]: ...
    def close(self) -> None: ...


class LiveSSESubscriber:
    """Real /v1/events SSE subscriber with explicit readiness barrier before writes."""

    def __init__(
        self,
        base_url: str,
        token: str,
        *,
        out_jsonl: Path | None = None,
    ) -> None:
        self._url = f"{base_url.rstrip('/')}/v1/events"
        self._token = token
        self._out_jsonl = Path(out_jsonl) if out_jsonl else None
        self._ready = threading.Event()
        self._stop = threading.Event()
        self._cond = threading.Condition()
        self._frames: list[dict[str, object]] = []
        self._response = None
        self._error: Exception | None = None
        self._thread = threading.Thread(target=self._run, daemon=True)

    @property
    def error(self) -> Exception | None:
        return self._error

    def start(self) -> None:
        self._thread.start()

    def wait_ready(self) -> bool:
        """Block until the SSE HTTP stream is open (or failed) before executing writes."""
        self._ready.wait()
        return self._error is None

    def frames(self) -> list[dict[str, object]]:
        with self._cond:
            return list(self._frames)

    def wait_for_frames(
        self,
        predicate: Callable[[list[dict[str, object]]], bool],
    ) -> list[dict[str, object]]:
        """Wait reactively until predicate(frames) holds or the stream closes."""
        with self._cond:
            while not self._stop.is_set() and not predicate(self._frames):
                self._cond.wait()
            return list(self._frames)

    def _run(self) -> None:
        req = urllib.request.Request(
            self._url,
            headers={
                "Authorization": f"Bearer {self._token}",
                "Accept": "text/event-stream",
            },
        )
        sink = self._out_jsonl.open("w", encoding="utf-8") if self._out_jsonl else None
        try:
            with urllib.request.urlopen(req) as resp:
                self._response = resp
                self._ready.set()
                for raw in resp:
                    if self._stop.is_set():
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
            self._error = exc
        finally:
            self._ready.set()
            self._stop.set()
            with self._cond:
                self._cond.notify_all()
            if sink is not None:
                with contextlib.suppress(OSError):
                    sink.close()

    def close(self) -> None:
        self._stop.set()
        if self._response is not None:
            with contextlib.suppress(OSError, AttributeError):
                self._response.close()
        with self._cond:
            self._cond.notify_all()
        if self._thread.is_alive():
            self._thread.join()
