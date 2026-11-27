"""Unit tests for CF03 sidecar family modules, shrinking, incident replays, and evidence manifests."""

from __future__ import annotations

import json
import os
from pathlib import Path
import sqlite3
import tempfile
import unittest
import urllib.parse

from catalog_feed_search.checker import CatalogItem, ProductCatalogAnswer
from catalog_feed_search.checker_types import CheckResult, Verdict
from catalog_feed_search.driver_sidecar import SidecarDriver
from catalog_feed_search.families.cf03_cli import build_parser
from catalog_feed_search.families.cf03_common import (
    FamilyReport,
    pick_free_port,
    shrink_catalog_failure,
    write_sha256sums,
)
from catalog_feed_search.families.cf03_k01_k02 import run_k01_sidecar, run_k02_sidecar
from catalog_feed_search.families.cf03_k03_k04_k07 import (
    _inspect_snapshot_db,
    run_k03_sidecar,
    run_k04_sidecar,
    run_k07_sidecar,
)
from catalog_feed_search.generators import BoundaryConfig, TreeEntrySpec, TreeSpec
from catalog_feed_search.references import reference_walk
from catalog_feed_search.seeds import BASE_SEED_PRIMARY, ReplaySelector


class _FakeSidecarTransport:
    """Deterministic in-memory HTTP transport mimicking D1 /v1 catalog & walk endpoints."""

    def __init__(self, boundary_cfg: BoundaryConfig) -> None:
        self._boundary = boundary_cfg
        self._projects: dict[str, dict[str, Path]] = {}
        self._counter = 0
        self._rev = 1

    def request(self, path: str, body: dict[str, object] | None = None, *, method: str = "GET") -> dict[str, object]:
        parsed = urllib.parse.urlsplit(path)
        qs = urllib.parse.parse_qs(parsed.query)
        if parsed.path == "/v1/projects" and method == "POST":
            self._counter += 1
            pid = f"proj-{self._counter}"
            roots_in = (body or {}).get("roots") or []
            root_map: dict[str, Path] = {}
            roots_out = []
            for idx, r in enumerate(roots_in):
                rid = f"root-{idx}"
                root_map[rid] = Path(str(r["path"]))
                roots_out.append({"id": rid, "path": str(r["path"])})
            self._projects[pid] = root_map
            return {"id": pid, "roots": roots_out}

        parts = parsed.path.strip("/").split("/")
        if len(parts) >= 5 and parts[0] == "v1" and parts[1] == "projects":
            pid = parts[2]
            root_map = self._projects[pid]
            sub = parts[4]
            if sub == "index":
                cov = []
                overall_state = "ready"
                for rid, rpath in root_map.items():
                    root_unreadable = not os.access(rpath, os.R_OK | os.X_OK)
                    failed_dirs = 1 if root_unreadable else self._count_unreadable_dirs(rpath)
                    st = "failed" if root_unreadable else "ready"
                    if st == "failed":
                        overall_state = "failed"
                    cov.append(
                        {
                            "root_id": rid,
                            "state": st,
                            "discovery_complete": not root_unreadable,
                            "refreshing": False,
                            "bounded_directories": 0,
                            "failed_directories": failed_dirs,
                            "error": "permission denied" if root_unreadable else "",
                        }
                    )
                return {"state": overall_state, "refreshing": False, "revision": self._rev, "coverage": cov}
            if sub == "search":
                rid_filter = qs.get("root_id", [""])[0]
                limit = int(qs.get("limit", ["50"])[0])
                cursor = qs.get("cursor", [""])[0]
                selected_roots = [(rid_filter, root_map[rid_filter])] if rid_filter else list(root_map.items())
                all_matches: list[dict[str, str]] = []
                cov_out: list[dict[str, object]] = []
                for rid, rpath in selected_roots:
                    root_unreadable = not os.access(rpath, os.R_OK | os.X_OK)
                    if root_unreadable:
                        cov_out.append(
                            {
                                "root_id": rid,
                                "state": "failed",
                                "discovery_complete": False,
                                "refreshing": False,
                                "bounded_directories": 0,
                                "failed_directories": 1,
                                "error": "permission denied",
                            }
                        )
                        continue
                    files, failed_dirs = self._safe_walk_files(rpath)
                    for p in files:
                        all_matches.append({"root_id": rid, "path": p})
                    cov_out.append(
                        {
                            "root_id": rid,
                            "state": "ready",
                            "discovery_complete": True,
                            "refreshing": False,
                            "bounded_directories": 0,
                            "failed_directories": failed_dirs,
                            "error": "",
                        }
                    )
                start = int(cursor) if cursor.isdigit() else 0
                page = all_matches[start : start + limit]
                next_cur = str(start + limit) if (start + limit) < len(all_matches) else ""
                return {
                    "revision": self._rev,
                    "matches": page,
                    "next_cursor": next_cur,
                    "coverage": cov_out,
                }
            if sub == "walk":
                return {"items": [], "next_cursor": ""}
        raise AssertionError(f"unexpected fake route: {method} {path}")

    def request_raw(self, path: str, body: dict[str, object] | None = None, *, method: str = "POST") -> tuple[bytes, dict[str, str]]:
        return b"", {}

    def _count_unreadable_dirs(self, root: Path) -> int:
        count = 0
        for dirpath, dirnames, _ in os.walk(root):
            for d in list(dirnames):
                full = Path(dirpath) / d
                if full.is_symlink():
                    dirnames.remove(d)
                    continue
                if not os.access(full, os.R_OK | os.X_OK):
                    count += 1
                    dirnames.remove(d)
        return count

    def _safe_walk_files(self, root: Path) -> tuple[list[str], int]:
        failed = self._count_unreadable_dirs(root)
        try:
            entries = reference_walk(root, boundary_config=self._boundary, include_hidden=True, prune_nested_checkouts=True)
            return [e.path for e in entries if e.kind == "file"], failed
        except PermissionError:
            # Walk skipping unreadable directories to mirror real sidecar fault recording.
            files: list[str] = []
            for dirpath, dirnames, filenames in os.walk(root):
                rel_dir = Path(dirpath).relative_to(root).as_posix()
                kept_dirs = []
                for d in sorted(dirnames):
                    full = Path(dirpath) / d
                    rel = d if rel_dir == "." else f"{rel_dir}/{d}"
                    if full.is_symlink() or self._boundary.is_boundary_dir(rel) or (full / ".git").exists():
                        continue
                    if not os.access(full, os.R_OK | os.X_OK):
                        continue
                    kept_dirs.append(d)
                dirnames[:] = kept_dirs
                for f in sorted(filenames):
                    full = Path(dirpath) / f
                    if full.is_symlink():
                        continue
                    rel = f if rel_dir == "." else f"{rel_dir}/{f}"
                    files.append(rel)
            return sorted(files), failed


class _FakeManagedSidecar:
    def __init__(self, driver: SidecarDriver) -> None:
        self._driver = driver
        self.restarts = 0

    def driver(self) -> SidecarDriver:
        return self._driver

    def restart(self) -> dict[str, object]:
        self.restarts += 1
        return {"restarts": self.restarts}


class TestCF03Families(unittest.TestCase):
    def setUp(self) -> None:
        self.boundary_cfg = BoundaryConfig(boundary_patterns=("node_modules", "target", ".git", "build", "dist"))
        self.transport = _FakeSidecarTransport(self.boundary_cfg)
        self.driver = SidecarDriver(self.transport)

    def test_pick_free_port_and_cli_parser(self) -> None:
        from catalog_feed_search.families.cf03_common import ManagedSidecar

        port = pick_free_port()
        self.assertGreater(port, 0)
        sc = ManagedSidecar(Path("/tmp/nonexistent-bin"), Path("/tmp/nonexistent-cfg"), token="tok")
        sc.url = f"http://127.0.0.1:{port}"
        self.assertIsInstance(sc.driver(), SidecarDriver)
        parser = build_parser()
        args = parser.parse_args(["--engine-bin", "/tmp/bin", "--out-dir", "/tmp/out", "--families", "K01,K02"])
        self.assertEqual(args.families, "K01,K02")
        self.assertEqual(args.seed, BASE_SEED_PRIMARY)

    def test_k01_and_k02_runners_with_fake_sidecar(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp)
            r1 = run_k01_sidecar(self.driver, self.boundary_cfg, out, count=4)
            self.assertEqual(r1.executed, 4)
            self.assertEqual(r1.failed, 0)
            self.assertEqual(r1.edge_executed, 8)
            self.assertEqual(r1.edge_failed, 0)

            r2, incidents = run_k02_sidecar(self.driver, self.boundary_cfg, out, count=6)
            self.assertEqual(r2.executed, 6)
            self.assertEqual(r2.failed, 0)
            self.assertEqual(r2.edge_executed, 6)
            self.assertEqual(r2.edge_failed, 0)
            self.assertEqual(len(r2.state_ledger), 6)
            self.assertEqual([i["incident_id"] for i in incidents], ["K02-INC-03", "K02-INC-04"])
            # Fake transport returns settled 2050 files immediately -> must be explicit unreproduced_attempt!
            self.assertEqual(incidents[0]["replay_disposition"], "unreproduced_attempt")
            self.assertEqual(incidents[0]["verdict"], "UNREPRODUCED_ATTEMPT")
            self.assertEqual(incidents[1]["replay_disposition"], "not_reproduced")
            self.assertEqual(incidents[1]["verdict"], "PASS")

    def test_decision_030_cold_query_not_excused_by_earlier_index_frame(self) -> None:
        """Decision 030: an earlier warming /source/index frame must NOT excuse a complete-looking empty /source/search."""

        class _CompleteEmptyAfterWarmingTransport(_FakeSidecarTransport):
            def __init__(self, boundary_cfg: BoundaryConfig) -> None:
                super().__init__(boundary_cfg)
                self._index_calls: dict[str, int] = {}

            def request(self, path: str, body: dict[str, object] | None = None, *, method: str = "GET") -> dict[str, object]:
                parsed = urllib.parse.urlsplit(path)
                parts = parsed.path.strip("/").split("/")
                if len(parts) >= 5 and parts[4] == "index":
                    pid = parts[2]
                    self._index_calls[pid] = self._index_calls.get(pid, 0) + 1
                    if self._index_calls[pid] == 1:
                        return {
                            "state": "warming",
                            "refreshing": True,
                            "revision": 1,
                            "coverage": [
                                {
                                    "root_id": "root-0",
                                    "state": "warming",
                                    "discovery_complete": False,
                                    "refreshing": True,
                                    "bounded_directories": 0,
                                    "failed_directories": 0,
                                    "error": "",
                                }
                            ],
                        }
                if len(parts) >= 5 and parts[4] == "search":
                    # Subsequent catalog answer claims ready & complete with 0 matches!
                    return {
                        "state": "ready",
                        "refreshing": False,
                        "revision": 2,
                        "matches": [],
                        "next_cursor": "",
                        "coverage": [
                            {
                                "root_id": "root-0",
                                "state": "ready",
                                "discovery_complete": True,
                                "refreshing": False,
                                "bounded_directories": 0,
                                "failed_directories": 0,
                                "error": "",
                            }
                        ],
                    }
                return super().request(path, body, method=method)

        bad_driver = SidecarDriver(_CompleteEmptyAfterWarmingTransport(self.boundary_cfg))
        with tempfile.TemporaryDirectory() as tmp:
            r2, incidents = run_k02_sidecar(bad_driver, self.boundary_cfg, Path(tmp), count=4)
            # Instance 3 is cold_query -> must be graded DEFECT, NOT excused by earlier warming frame!
            self.assertGreaterEqual(r2.failed, 1)
            cold_entries = [e for e in r2.state_ledger if e.get("attempted_state") == "cold_query"]
            self.assertEqual(len(cold_entries), 1)
            self.assertEqual(cold_entries[0]["verdict"], "DEFECT")
            self.assertEqual(cold_entries[0]["transition_evidence"]["earlier_index_state"], "warming")
            # K02-INC-03 must report reproduced / DEFECT
            self.assertEqual(incidents[0]["replay_disposition"], "reproduced")
            self.assertEqual(incidents[0]["verdict"], "DEFECT")

    def test_k03_k04_k07_runners_with_fake_sidecar(self) -> None:
        fake_sc = _FakeManagedSidecar(self.driver)
        r3 = run_k03_sidecar(fake_sc, self.boundary_cfg, count=4)  # type: ignore[arg-type]
        self.assertEqual(r3.executed, 4)
        self.assertEqual(r3.failed, 0)
        self.assertGreaterEqual(fake_sc.restarts, 3)

        r4 = run_k04_sidecar(self.driver, self.boundary_cfg, count=4)
        self.assertEqual(r4.executed, 4)
        self.assertEqual(r4.failed, 0)
        self.assertEqual(r4.edge_executed, 8)
        self.assertEqual(r4.edge_failed, 0)

        with tempfile.TemporaryDirectory() as cfg_tmp:
            cfg = Path(cfg_tmp)
            # Create minimal SQLite store.db matching snapshot schema.
            conn = sqlite3.connect(cfg / "store.db")
            conn.executescript(
                """
                CREATE TABLE source_snapshots (id TEXT);
                CREATE TABLE source_snapshot_boundaries (snapshot_id TEXT);
                CREATE TABLE source_manifests (id TEXT);
                CREATE TABLE source_manifest_chunks (id TEXT);
                CREATE TABLE source_manifest_layout (manifest_id TEXT, chunk_id TEXT);
                """
            )
            conn.commit()
            conn.close()
            r7 = run_k07_sidecar(self.driver, cfg, count=4)
            self.assertEqual(r7.executed, 4)
            self.assertEqual(r7.failed, 0)
            self.assertEqual(r7.edge_executed, 4)
            self.assertEqual(r7.edge_failed, 0)
            self.assertEqual(_inspect_snapshot_db(cfg)["dangling_chunks"], 0)

    def test_shrink_catalog_failure_and_sha256sums(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp)
            sel = ReplaySelector(BASE_SEED_PRIMARY, "K01", 99)
            spec = TreeSpec(
                selector=sel,
                entries=(
                    TreeEntrySpec("ok1.txt", "file", b"a\n"),
                    TreeEntrySpec("bad.txt", "file", b"b\n"),
                    TreeEntrySpec("ok2.txt", "file", b"c\n"),
                ),
            )

            def _eval_defect(cand: TreeSpec) -> CheckResult:
                if any(e.path == "bad.txt" for e in cand.entries):
                    return CheckResult(Verdict.DEFECT, "catalog", missing_claims=("root-0:bad.txt",), reason="missing bad.txt")
                return CheckResult(Verdict.PASS, "catalog")

            sr = shrink_catalog_failure(sel, spec, "missing bad.txt", _eval_defect, out)
            self.assertIsNotNone(sr)
            assert sr is not None
            self.assertEqual(sr.shrunk_entry_count, 1)
            sums = write_sha256sums(out)
            self.assertTrue(sums.is_file())
            self.assertIn("shrunk/K01_20261127_99.json", sums.read_text(encoding="utf-8"))

            rep = FamilyReport("K01", "sidecar", BASE_SEED_PRIMARY, 1, 0)
            rep.record_generated(CheckResult(Verdict.PASS_DECLARED_GAP, "catalog", accounted_gaps=("catalog_failed",)))
            self.assertEqual(rep.declared_gap_rate, 1.0)
            self.assertEqual(json.loads(json.dumps(rep.to_dict()))["declared_gaps"], {"catalog_failed": 1})


if __name__ == "__main__":  # pragma: no cover
    unittest.main()
