"""Fixture-owned barriers and explicit short timer guards for wait/watch sidecar lanes."""
from __future__ import annotations

import hashlib
import http.server
import json
import os
import pathlib
import shlex
import socket
import subprocess
import threading
import time
from typing import Any, Callable


class BarrierTimeoutError(RuntimeError):
    """Raised when a fixture barrier or poll exceeds its explicit short timer guard."""

    def __init__(
        self,
        message: str,
        *,
        phase: str,
        verdict: str = "incomplete",
        elapsed_s: float = 0.0,
        diagnostics: dict[str, Any] | None = None,
    ) -> None:
        super().__init__(message)
        self.phase = phase
        self.verdict = verdict if verdict in {"inconclusive", "incomplete"} else "incomplete"
        self.elapsed_s = elapsed_s
        self.diagnostics = diagnostics or {}

    def to_dict(self) -> dict[str, Any]:
        return {
            "verdict": self.verdict,
            "phase": self.phase,
            "error": str(self),
            "elapsed_s": round(self.elapsed_s, 4),
            "diagnostics": self.diagnostics,
        }


class TimerGuard:
    """Explicit short timer guard that aborts cleanly and dumps diagnostic state on expiry."""

    def __init__(
        self,
        timeout_s: float,
        *,
        phase: str,
        verdict: str = "incomplete",
        dump_state: Callable[[], dict[str, Any]] | None = None,
        poll_interval_s: float = 0.05,
    ) -> None:
        if timeout_s <= 0:
            raise ValueError("timeout_s must be positive")
        self.timeout_s = float(timeout_s)
        self.phase = phase
        self.verdict = verdict
        self.dump_state = dump_state
        self.poll_interval_s = max(0.01, float(poll_interval_s))
        self.started_monotonic = time.monotonic()

    def elapsed_s(self) -> float:
        return time.monotonic() - self.started_monotonic

    def remaining_s(self) -> float:
        return max(0.0, self.timeout_s - self.elapsed_s())

    def check(self, detail: str = "") -> None:
        elapsed = self.elapsed_s()
        if elapsed >= self.timeout_s:
            diag: dict[str, Any] = {}
            if self.dump_state is not None:
                try:
                    diag = self.dump_state() or {}
                except Exception as exc:  # pragma: no cover - defensive
                    diag = {"dump_error": str(exc)}
            msg = f"timer guard expired during {self.phase} after {elapsed:.2f}s"
            if detail:
                msg = f"{msg}: {detail}"
            raise BarrierTimeoutError(
                msg,
                phase=self.phase,
                verdict=self.verdict,
                elapsed_s=elapsed,
                diagnostics=diag,
            )

    def poll(self, predicate: Callable[[], Any], *, detail: str = "") -> Any:
        """Poll predicate until truthy or the short timer guard expires."""
        while True:
            val = predicate()
            if val:
                return val
            self.check(detail)
            time.sleep(min(self.poll_interval_s, max(0.01, self.remaining_s())))


class ProcessBarrier:
    """Fixture-owned process barrier (C01): writes `reached`, waits for `release`, exits with exact code."""

    def __init__(
        self,
        root_dir: pathlib.Path | str,
        *,
        exit_code: int = 17,
        marker: str = "ww02-c01-exit-17",
        payload: str | None = None,
        max_script_wait_s: int = 90,
    ) -> None:
        self.root_dir = pathlib.Path(root_dir).resolve()
        self.root_dir.mkdir(parents=True, exist_ok=True)
        self.reached_path = self.root_dir / "reached"
        self.release_path = self.root_dir / "release"
        self.exit_code = int(exit_code)
        self.marker = marker
        self.payload = payload if payload is not None else f"{marker}:stdout:ok\n"
        self.max_script_wait_s = int(max_script_wait_s)
        self.reached_at_ns: int | None = None
        self.released_at_ns: int | None = None
        self.child_pid: int | None = None
        self._reached_payload: dict[str, Any] = {}

    @property
    def payload_sha256(self) -> str:
        return hashlib.sha256(self.payload.encode("utf-8")).hexdigest()

    def command_string(self) -> str:
        """Return a portable POSIX shell command implementing the barrier contract."""
        reached_q = shlex.quote(str(self.reached_path))
        reached_tmp_q = shlex.quote(str(self.reached_path) + ".tmp")
        release_q = shlex.quote(str(self.release_path))
        marker_q = shlex.quote(self.marker)
        payload_q = shlex.quote(self.payload)
        max_ticks = max(10, self.max_script_wait_s * 20)
        # POSIX sh only; writes atomic reached sentinel, polls release gate, emits exact payload, exits with exit_code.
        return (
            f"printf '{{\"marker\":%s,\"pid\":%d,\"ts\":%s}}\\n' "
            f"\"\\\"{self.marker}\\\"\" \"$$\" \"$(date +%s)\" > {reached_tmp_q} "
            f"&& mv {reached_tmp_q} {reached_q}; "
            f"i=0; while [ ! -f {release_q} ]; do "
            f"i=$((i+1)); if [ \"$i\" -ge {max_ticks} ]; then exit 124; fi; "
            f"sleep 0.05; done; "
            f"printf '%s' {payload_q}; "
            f"exit {self.exit_code}"
        )

    def is_reached(self) -> bool:
        return self.reached_path.is_file()

    def wait_reached(
        self,
        timeout_s: float = 15.0,
        *,
        dump_state: Callable[[], dict[str, Any]] | None = None,
    ) -> dict[str, Any]:
        guard = TimerGuard(
            timeout_s,
            phase="barrier.wait_reached",
            verdict="incomplete",
            dump_state=dump_state,
        )
        guard.poll(self.is_reached, detail=f"sentinel {self.reached_path.name} not written")
        self.reached_at_ns = time.time_ns()
        raw = self.reached_path.read_text(encoding="utf-8", errors="replace").strip()
        try:
            parsed = json.loads(raw) if raw.startswith("{") else {"raw": raw}
        except ValueError:
            parsed = {"raw": raw}
        self._reached_payload = parsed
        pid = parsed.get("pid")
        if isinstance(pid, int):
            self.child_pid = pid
        return parsed

    def release(self) -> dict[str, Any]:
        """Atomically write the driver-owned `release` gate file."""
        if not self.is_reached():
            raise RuntimeError("cannot release ProcessBarrier before `reached` sentinel is verified")
        tmp = self.release_path.with_suffix(".tmp")
        released_ns = time.time_ns()
        body = json.dumps({"marker": self.marker, "released_at_ns": released_ns})
        tmp.write_text(body, encoding="utf-8")
        os.replace(tmp, self.release_path)
        self.released_at_ns = released_ns
        return {"released": True, "released_at_ns": released_ns}

    def source_facts(self) -> dict[str, Any]:
        return {
            "barrier_kind": "process_file_gate",
            "marker": self.marker,
            "expected_exit_code": self.exit_code,
            "expected_payload": self.payload,
            "expected_payload_sha256": self.payload_sha256,
            "reached": self.is_reached(),
            "released": self.release_path.is_file(),
            "reached_at_ns": self.reached_at_ns,
            "released_at_ns": self.released_at_ns,
            "child_pid": self.child_pid,
            "reached_sentinel": self._reached_payload,
        }


class HTTPReadinessFixture:
    """Extensible loopback HTTP readiness barrier for C05: returns 503 until released, then 200."""

    def __init__(self, *, path: str = "/ready", ready_body: str = "ready\n") -> None:
        self.path = path
        self.ready_body = ready_body
        self._lock = threading.Lock()
        self._ready = False
        self._probes: list[dict[str, Any]] = []
        self._server: http.server.ThreadingHTTPServer | None = None
        self._thread: threading.Thread | None = None
        self.port: int = 0
        self.released_at_ns: int | None = None

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self.port}{self.path}"

    def start(self) -> str:
        fixture = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self) -> None:  # noqa: N802
                with fixture._lock:
                    ready = fixture._ready
                    status = 200 if (ready and self.path == fixture.path) else 503
                    fixture._probes.append({
                        "path": self.path,
                        "status": status,
                        "ts_ns": time.time_ns(),
                    })
                body = fixture.ready_body.encode("utf-8") if status == 200 else b"not_ready\n"
                self.send_response(status)
                self.send_header("Content-Type", "text/plain")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, format: str, *args: Any) -> None:  # noqa: A002
                return

        self._server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.port = self._server.server_address[1]
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()
        return self.url

    def release(self) -> dict[str, Any]:
        with self._lock:
            self._ready = True
            self.released_at_ns = time.time_ns()
        return {"released": True, "url": self.url, "released_at_ns": self.released_at_ns}

    def probes(self) -> list[dict[str, Any]]:
        with self._lock:
            return list(self._probes)

    def source_facts(self) -> dict[str, Any]:
        with self._lock:
            return {
                "barrier_kind": "http_readiness",
                "url": self.url,
                "ready": self._ready,
                "released_at_ns": self.released_at_ns,
                "probe_count": len(self._probes),
                "probes": list(self._probes),
            }

    def close(self) -> None:
        if self._server is not None:
            self._server.shutdown()
            self._server.server_close()
            self._server = None
        if self._thread is not None:
            self._thread.join(timeout=3)
            self._thread = None


class GitRefWriterFixture:
    """Extensible Git ref writer barrier for C16: initializes a local repo and advances a ref on release."""

    def __init__(self, repo_dir: pathlib.Path | str, *, branch: str = "main") -> None:
        self.repo_dir = pathlib.Path(repo_dir).resolve()
        self.branch = branch
        self.commits: list[dict[str, Any]] = []

    def _git(self, *args: str) -> str:
        env = {
            **os.environ,
            "GIT_AUTHOR_NAME": "WW02 Fixture",
            "GIT_AUTHOR_EMAIL": "ww02@example.invalid",
            "GIT_COMMITTER_NAME": "WW02 Fixture",
            "GIT_COMMITTER_EMAIL": "ww02@example.invalid",
        }
        proc = subprocess.run(
            ["git", *args],
            cwd=self.repo_dir,
            env=env,
            capture_output=True,
            text=True,
            check=True,
        )
        return proc.stdout.strip()

    def init(self) -> str:
        self.repo_dir.mkdir(parents=True, exist_ok=True)
        self._git("init", "-b", self.branch)
        (self.repo_dir / "state.txt").write_text("initial\n", encoding="utf-8")
        self._git("add", "state.txt")
        self._git("commit", "-m", "initial fixture commit")
        oid = self._git("rev-parse", "HEAD")
        self.commits.append({"seq": 1, "oid": oid, "ts_ns": time.time_ns(), "message": "initial fixture commit"})
        return oid

    def release(self, *, message: str = "advance ref") -> dict[str, Any]:
        seq = len(self.commits) + 1
        (self.repo_dir / "state.txt").write_text(f"step-{seq}\n", encoding="utf-8")
        self._git("add", "state.txt")
        self._git("commit", "-m", f"{message} #{seq}")
        oid = self._git("rev-parse", "HEAD")
        rec = {"seq": seq, "oid": oid, "ts_ns": time.time_ns(), "message": f"{message} #{seq}"}
        self.commits.append(rec)
        return rec

    def source_facts(self) -> dict[str, Any]:
        return {
            "barrier_kind": "git_ref_writer",
            "branch": self.branch,
            "head_oid": self.commits[-1]["oid"] if self.commits else "",
            "commit_count": len(self.commits),
            "commits": list(self.commits),
        }
