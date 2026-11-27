"""Shared types, summary accounting, and sidecar helpers for CF06 search families."""

from __future__ import annotations

from collections import Counter
from dataclasses import dataclass, field
import time
from typing import Any
import urllib.error

from ..checker_types import Verdict
from ..driver_http import SidecarTransport
from ..driver_sidecar import SidecarDriver


class RateLimitedTransport:
    """Thin SidecarTransport wrapper that retries HTTP 429 with bounded backoff."""

    def __init__(self, inner: SidecarTransport, *, max_retries: int = 8, base_sleep: float = 0.25) -> None:
        self._inner = inner
        self._max_retries = max_retries
        self._base_sleep = base_sleep

    def request(self, path: str, payload: dict[str, object] | None = None, *, method: str = "POST") -> dict[str, object]:
        for attempt in range(self._max_retries + 1):
            try:
                return self._inner.request(path, payload, method=method)
            except urllib.error.HTTPError as exc:
                if exc.code != 429 or attempt >= self._max_retries:
                    raise
                time.sleep(self._base_sleep * (attempt + 1))
        raise RuntimeError("unreachable")  # pragma: no cover

    def request_raw(
        self,
        path: str,
        payload: dict[str, object] | None = None,
        *,
        method: str = "POST",
    ) -> tuple[bytes, dict[str, str]]:
        for attempt in range(self._max_retries + 1):
            try:
                return self._inner.request_raw(path, payload, method=method)
            except urllib.error.HTTPError as exc:
                if exc.code != 429 or attempt >= self._max_retries:
                    raise
                time.sleep(self._base_sleep * (attempt + 1))
        raise RuntimeError("unreachable")  # pragma: no cover


@dataclass
class FamilySummary:
    """Planned/executed/verdict counts and declared-gap rates for one CF06 family."""

    family_id: str
    lane: str
    planned: int
    executed: int = 0
    passed: int = 0
    failed: int = 0
    blocked: int = 0
    inconclusive: int = 0
    edges_planned: int = 0
    edges_passed: int = 0
    edges_failed: int = 0
    gap_counts: Counter[str] = field(default_factory=Counter)
    failures: list[dict[str, object]] = field(default_factory=list)
    shrunk_files: list[str] = field(default_factory=list)

    def record_verdict(self, verdict: Verdict, accounted_gaps: tuple[str, ...] = ()) -> None:
        self.executed += 1
        if verdict == Verdict.PASS:
            self.passed += 1
        elif verdict == Verdict.PASS_DECLARED_GAP:
            self.passed += 1
            for g in accounted_gaps:
                self.gap_counts[g] += 1
        elif verdict == Verdict.DEFECT:
            self.failed += 1
        else:
            self.inconclusive += 1

    def record_pass(self) -> None:
        self.executed += 1
        self.passed += 1

    def record_fail(self, detail: dict[str, object]) -> None:
        self.executed += 1
        self.failed += 1
        if len(self.failures) < 20:
            self.failures.append(detail)

    def record_edge(self, ok: bool, detail: dict[str, object] | None = None) -> None:
        if ok:
            self.edges_passed += 1
        else:
            self.edges_failed += 1
            if detail and len(self.failures) < 20:
                self.failures.append(detail)

    def declared_gap_rate(self) -> float:
        if self.executed <= 0:
            return 0.0
        total_gaps = sum(self.gap_counts.values())
        return round(total_gaps / self.executed, 6)

    def to_dict(self) -> dict[str, object]:
        return {
            "family_id": self.family_id,
            "lane": self.lane,
            "planned": self.planned,
            "executed": self.executed,
            "passed": self.passed,
            "failed": self.failed,
            "blocked": self.blocked,
            "inconclusive": self.inconclusive,
            "edges_planned": self.edges_planned,
            "edges_passed": self.edges_passed,
            "edges_failed": self.edges_failed,
            "declared_gap_counts": dict(sorted(self.gap_counts.items())),
            "declared_gap_rate": self.declared_gap_rate(),
            "failures": self.failures,
            "shrunk_files": self.shrunk_files,
        }


def wait_for_catalog_ready(
    driver: SidecarDriver,
    project_id: str,
    timeout_s: float = 30.0,
    poll_s: float = 0.05,
) -> dict[str, Any]:
    """Poll /v1/projects/{id}/source/index until all roots report state=ready and discovery_complete=true."""
    deadline = time.monotonic() + timeout_s
    last: dict[str, Any] = {}
    while time.monotonic() < deadline:
        last = driver.get_source_index(project_id)
        cov = last.get("coverage") or []
        if isinstance(cov, list) and cov:
            all_ready = all(
                isinstance(c, dict)
                and str(c.get("state") or "").lower() == "ready"
                and c.get("discovery_complete") is not False
                and c.get("refreshing") is not True
                for c in cov
            )
            if all_ready:
                return last
        time.sleep(poll_s)
    return last


def build_code_dsl(pattern: str, *, regex: bool = False, path_filters: tuple[str, ...] = ()) -> str:
    """Format a CodeQuerySpec into a D1 /v1/search DSL query string."""
    parts: list[str] = []
    if regex or any(ch.isspace() for ch in pattern):
        escaped = pattern.replace("\\", "\\\\").replace('"', '\\"')
        parts.append(f'"{escaped}"')
    else:
        parts.append(pattern)
    for pf in path_filters:
        parts.append(f"path:{pf}")
    return " ".join(parts)
