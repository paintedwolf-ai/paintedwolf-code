"""Unit tests for CF06 search-leg runners, types, and CLI."""

from __future__ import annotations

import json
from pathlib import Path
import tempfile
import unittest
import urllib.error

from catalog_feed_search.checker_types import Verdict
from catalog_feed_search.driver_sidecar import SidecarDriver
from catalog_feed_search.families.cf06_cli import (
    LEDGER_SIDECAR_COUNTS,
    build_parser,
    run_cf06_sidecar_suite,
)
from catalog_feed_search.families.cf06_types import (
    FamilySummary,
    RateLimitedTransport,
    build_code_dsl,
    wait_for_catalog_ready,
)


class _FakeTransport:
    """In-memory fake SidecarTransport for unit testing CF06 runners and CLI."""

    def __init__(self) -> None:
        self.projects: dict[str, list[Path]] = {}
        self.next_pid = 0

    def request(self, path: str, payload: dict[str, object] | None = None, *, method: str = "POST") -> dict[str, object]:
        if path == "/v1/projects" and method == "POST":
            self.next_pid += 1
            pid = f"proj-{self.next_pid}"
            roots_in = (payload or {}).get("roots") or []
            root_paths = [Path(str(r["path"])) for r in roots_in if isinstance(r, dict)]
            self.projects[pid] = root_paths
            return {
                "id": pid,
                "roots": [{"id": f"root-{i}", "path": str(p)} for i, p in enumerate(root_paths)],
            }
        if path.endswith("/source/index"):
            return {"revision": 1, "coverage": [{"root_id": "root-0", "state": "ready", "discovery_complete": True, "refreshing": False}]}
        if "/source/search?" in path:
            return {"revision": 1, "matches": [], "coverage": [{"root_id": "root-0", "state": "ready"}]}
        if path == "/v1/search":
            q = str((payload or {}).get("query") or "")
            if "kind:bogus" in q or "kind:invalid" in q or "kind:code kind:symbol" in q or "NOT kind:code" in q:
                raise urllib.error.HTTPError(path, 400, "bad query", hdrs={}, fp=None)  # type: ignore[arg-type]
            if (payload or {}).get("cursor") == "invalid-cursor-token":
                raise urllib.error.HTTPError(path, 400, "bad cursor", hdrs={}, fp=None)  # type: ignore[arg-type]
            if (payload or {}).get("origin_project_id") == "00000000-0000-0000-0000-000000000000":
                raise urllib.error.HTTPError(path, 404, "not found", hdrs={}, fp=None)  # type: ignore[arg-type]
            return {"status": "complete", "hits": [], "issues": []}
        if "/editor-documents" in path and method == "POST":
            return {"id": "doc-1", "revision": 1}
        if "/editor-documents/" in path and method == "PUT":
            return {"id": "doc-1", "revision": 2}
        return {}

    def request_raw(self, path: str, payload: dict[str, object] | None = None, *, method: str = "POST") -> tuple[bytes, dict[str, str]]:
        return b"", {"x-export-truncated": "false"}


class TestCF06TypesAndCLI(unittest.TestCase):
    def test_family_summary_accounting_and_gap_rate(self) -> None:
        s = FamilySummary(family_id="S01", lane="sidecar", planned=4, edges_planned=2)
        s.record_verdict(Verdict.PASS)
        s.record_verdict(Verdict.PASS_DECLARED_GAP, ("catalog_refreshing",))
        s.record_verdict(Verdict.DEFECT)
        s.record_verdict(Verdict.UNKNOWN)
        s.record_edge(True)
        s.record_edge(False, {"edge": "e2"})
        d = s.to_dict()
        self.assertEqual(d["executed"], 4)
        self.assertEqual(d["passed"], 2)
        self.assertEqual(d["failed"], 1)
        self.assertEqual(d["inconclusive"], 1)
        self.assertEqual(d["declared_gap_counts"], {"catalog_refreshing": 1})
        self.assertAlmostEqual(float(d["declared_gap_rate"]), 0.25)
        self.assertEqual(d["edges_passed"], 1)
        self.assertEqual(d["edges_failed"], 1)

    def test_build_code_dsl_formats_literal_regex_and_path(self) -> None:
        self.assertEqual(build_code_dsl("alpha"), "alpha")
        self.assertEqual(build_code_dsl("(a|b)", regex=True), '"(a|b)"')
        self.assertEqual(build_code_dsl("alpha", path_filters=("src/",)), "alpha path:src/")

    def test_wait_for_catalog_ready_returns_on_ready(self) -> None:
        driver = SidecarDriver(_FakeTransport())
        res = wait_for_catalog_ready(driver, "proj-1", timeout_s=1.0, poll_s=0.01)
        self.assertEqual(res["revision"], 1)

    def test_ledger_sidecar_counts_match_campaign_ledger(self) -> None:
        self.assertEqual(
            LEDGER_SIDECAR_COUNTS,
            {"S01": 300, "S02": 200, "S03": 100, "S04": 200, "S05": 100, "S06": 200, "S07": 60, "S08": 60},
        )
        self.assertEqual(sum(LEDGER_SIDECAR_COUNTS.values()), 1220)

    def test_cli_parser_defaults(self) -> None:
        parser = build_parser()
        args = parser.parse_args(["--url", "http://127.0.0.1:9999", "--token", "tok", "--out-dir", "/tmp/out"])
        self.assertEqual(args.seed, "20261127")
        self.assertEqual(args.scale, 1.0)
        self.assertIn("S01", args.families)

    def test_run_s06_and_s08_against_fake_transport(self) -> None:
        driver = SidecarDriver(_FakeTransport())
        with tempfile.TemporaryDirectory() as tmp:
            summaries = run_cf06_sidecar_suite(
                driver,
                Path(tmp),
                families=("S06", "S08"),
                counts={"S06": 8, "S08": 4},
            )
        self.assertEqual(len(summaries), 2)
        s06, s08 = summaries
        self.assertEqual(s06.passed, 8)
        self.assertEqual(s06.edges_passed, 10)
        self.assertEqual(s08.passed, 4)
        self.assertEqual(s08.edges_passed, 4)

    def test_unsupported_family_raises(self) -> None:
        driver = SidecarDriver(_FakeTransport())
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaises(ValueError):
                run_cf06_sidecar_suite(driver, Path(tmp), families=("S99",), counts={"S99": 1})

    def test_rate_limited_transport_retries_429(self) -> None:
        class _Flaky429:
            def __init__(self) -> None:
                self.calls = 0

            def request(self, path: str, payload: dict[str, object] | None = None, *, method: str = "POST") -> dict[str, object]:
                self.calls += 1
                if self.calls < 2:
                    raise urllib.error.HTTPError(path, 429, "rate limited", hdrs={}, fp=None)  # type: ignore[arg-type]
                return {"ok": True}

            def request_raw(self, path: str, payload: dict[str, object] | None = None, *, method: str = "POST") -> tuple[bytes, dict[str, str]]:
                self.calls += 1
                if self.calls < 4:
                    raise urllib.error.HTTPError(path, 429, "rate limited", hdrs={}, fp=None)  # type: ignore[arg-type]
                return b"ok", {}

        wrapped = RateLimitedTransport(_Flaky429(), max_retries=3, base_sleep=0.001)
        self.assertEqual(wrapped.request("/v1/search"), {"ok": True})
        self.assertEqual(wrapped.request_raw("/v1/search/export"), (b"ok", {}))


if __name__ == "__main__":  # pragma: no cover
    unittest.main()
