"""Unit tests for CF08 corpus generators and sidecar R01-R04 runner."""

from __future__ import annotations

from pathlib import Path
import tempfile
import unittest

from catalog_feed_search.families.cf08_corpus import (
    FamilySummary,
    collect_disk_hashes,
    encode_text_with_bom,
    generate_r02_case,
    generate_r03_case,
    generate_r04_case,
    sha256_bytes,
)
from catalog_feed_search.families.cf08_replace import CF08SidecarRunner
from catalog_feed_search.queries import ReplaceSpec
from catalog_feed_search.references import SUPPORTED_REPLACE_ENCODINGS, reference_replace


class _FakeSidecarTransport:
    """In-memory mock SidecarTransport exercising R01-R04 contracts."""

    def __init__(self, *, inject_r03_defect: bool = False) -> None:
        self.projects: dict[str, tuple[str, Path]] = {}
        self.docs: dict[str, dict[str, object]] = {}
        self.versions: dict[str, list[dict[str, object]]] = {}
        self.version_bytes: dict[str, bytes] = {}
        self.file_ids: dict[tuple[str, str], str] = {}
        self.inject_r03_defect = inject_r03_defect
        self._counter = 0

    def _next_id(self, prefix: str) -> str:
        self._counter += 1
        return f"{prefix}-{self._counter:04d}"

    def request_raw(self, path: str, body=None, method: str = "POST"):
        return b"", {}

    def request(self, path: str, body=None, method: str = "POST") -> dict[str, object]:
        if path == "/v1/projects" and method == "POST":
            pid = self._next_id("proj")
            rid = self._next_id("root")
            root_path = Path(body["roots"][0]["path"])
            self.projects[pid] = (rid, root_path)
            return {"id": pid, "roots": [{"id": rid, "path": str(root_path)}]}

        if path.endswith("/source/index") and method == "GET":
            pid = path.split("/")[3]
            rid, _ = self.projects[pid]
            return {
                "revision": 1,
                "state": "ready",
                "refreshing": False,
                "coverage": [{"root_id": rid, "state": "ready", "discovery_complete": True, "refreshing": False}],
            }

        if path.endswith("/removal-assessment") and method == "GET":
            return {"assessment_token": "tok-1"}

        if path.endswith("/removals") and method == "POST":
            return {"project_state": "deleted"}

        if path == "/v1/search/replace/preview" and method == "POST":
            pid = str(body["origin_project_id"])
            rid, root_dir = self.projects[pid]
            raw_q = str(body["query"])
            if bool(body.get("regex")) and len(raw_q) >= 2 and raw_q[0] == '"' and raw_q[-1] == '"':
                raw_q = raw_q[1:-1]
            spec = ReplaceSpec(
                pattern=raw_q,
                replacement=str(body["replacement"]),
                regex=bool(body.get("regex")),
                case_sensitive=bool(body.get("case_sensitive")),
                whole_word=bool(body.get("whole_word")),
            )
            issues: list[dict[str, object]] = []
            files_out: list[dict[str, object]] = []
            truncated = False
            total_hunks = 0
            readable_dir = Path(tempfile.mkdtemp(prefix="cf08_mock_"))
            try:
                for p in sorted(root_dir.rglob("*")):
                    if not p.is_file():
                        continue
                    rel = p.relative_to(root_dir).as_posix()
                    try:
                        raw = p.read_bytes()
                    except PermissionError:
                        issues.append({"executor": "code", "reason": "files_skipped", "count": 1})
                        continue
                    dst = readable_dir / rel
                    dst.parent.mkdir(parents=True, exist_ok=True)
                    dst.write_bytes(raw)
                ref = reference_replace(readable_dir, spec, root_id=rid)
                for rel, res in sorted(ref.items()):
                    cnt = res.match_count
                    if total_hunks + cnt > 5000:
                        truncated = True
                        issues.append({"executor": "code", "reason": "result_limit"})
                        break
                    total_hunks += cnt
                    files_out.append(
                        {
                            "root_id": rid,
                            "path": rel,
                            "sha256": res.before_sha256,
                            "hunks": [{"line": 1, "end_line": 1, "before": spec.pattern, "after": spec.replacement}] * cnt,
                        }
                    )
            finally:
                import shutil
                shutil.rmtree(readable_dir, ignore_errors=True)
            state = "limited" if issues else "ready"
            return {"state": state, "files": files_out, "truncated": truncated, "issues": issues}

        if path == "/v1/search/replace/apply" and method == "POST":
            pid = str(body["origin_project_id"])
            rid, root_dir = self.projects[pid]
            raw_q = str(body["query"])
            if bool(body.get("regex")) and len(raw_q) >= 2 and raw_q[0] == '"' and raw_q[-1] == '"':
                raw_q = raw_q[1:-1]
            spec = ReplaceSpec(
                pattern=raw_q,
                replacement=str(body["replacement"]),
                regex=bool(body.get("regex")),
                case_sensitive=bool(body.get("case_sensitive")),
                whole_word=bool(body.get("whole_word")),
            )
            readable_dir = Path(tempfile.mkdtemp(prefix="cf08_mock_apply_"))
            try:
                for p in sorted(root_dir.rglob("*")):
                    if not p.is_file():
                        continue
                    rel = p.relative_to(root_dir).as_posix()
                    try:
                        raw = p.read_bytes()
                    except PermissionError:
                        continue
                    dst = readable_dir / rel
                    dst.parent.mkdir(parents=True, exist_ok=True)
                    dst.write_bytes(raw)
                ref = reference_replace(readable_dir, spec, root_id=rid)
            finally:
                import shutil
                shutil.rmtree(readable_dir, ignore_errors=True)
            outcomes: list[dict[str, object]] = []
            for item in body["files"]:
                rel = str(item["path"])
                full = root_dir / rel
                if not full.is_file():
                    outcomes.append({"root_id": rid, "path": rel, "applied": False, "skipped": True, "reason": "source_not_found"})
                    continue
                raw = full.read_bytes()
                if sha256_bytes(raw) != str(item["sha256"]):
                    outcomes.append({"root_id": rid, "path": rel, "applied": False, "skipped": True, "reason": "source_write_conflict"})
                    continue
                after_bytes = ref[rel].resulting_bytes
                if self.inject_r03_defect:
                    after_bytes = after_bytes + b"CORRUPT"
                full.write_bytes(after_bytes)
                fid = self.file_ids.setdefault((pid, rel), self._next_id("file"))
                v_before = self._next_id("ver")
                v_after = self._next_id("ver")
                self.version_bytes[v_before] = raw
                self.version_bytes[v_after] = after_bytes
                self.versions.setdefault(fid, []).extend(
                    [
                        {"id": v_before, "content_sha256": sha256_bytes(raw)},
                        {"id": v_after, "content_sha256": sha256_bytes(after_bytes), "origin": "user"},
                    ]
                )
                outcomes.append({"root_id": rid, "path": rel, "applied": True, "skipped": False, "matches": ref[rel].match_count})
            return {"batch_id": body["operation_id"], "files": outcomes}

        if "/editor-documents" in path and method == "POST":
            pid = path.split("/")[3]
            rel = str(body["path"])
            rid = str(body["root_id"])
            fid = self.file_ids.setdefault((pid, rel), self._next_id("file"))
            doc = self.docs.get(f"{pid}:{rel}")
            if doc is None:
                doc = {"id": self._next_id("doc"), "file_id": fid, "root_id": rid, "path": rel, "revision": 1, "dirty": False}
                self.docs[f"{pid}:{rel}"] = doc
            return doc

        if "/editor-documents/" in path and method == "PUT":
            doc_id = path.split("/")[-1]
            for doc in self.docs.values():
                if doc["id"] == doc_id:
                    doc["revision"] = int(doc["revision"]) + 1
                    doc["dirty"] = True
                    return doc
            raise KeyError(doc_id)

        if "/source?" in path and method == "GET":
            pid = path.split("/")[3]
            rid, root_dir = self.projects[pid]
            from urllib.parse import parse_qs, urlparse
            qs = parse_qs(urlparse(path).query)
            rel = qs["path"][0]
            raw = (root_dir / rel).read_bytes()
            fid = self.file_ids.setdefault((pid, rel), self._next_id("file"))
            return {"file_id": fid, "sha256": sha256_bytes(raw), "encoding": "utf-8"}

        if path.endswith("/source") and method == "PUT":
            pid = path.split("/")[3]
            _, root_dir = self.projects[pid]
            (root_dir / str(body["path"])).write_bytes(str(body["content"]).encode("utf-8"))
            return {"sha256": sha256_bytes(str(body["content"]).encode("utf-8"))}

        if "/source/versions?" in path and method == "GET":
            from urllib.parse import parse_qs, urlparse
            qs = parse_qs(urlparse(path).query)
            fid = qs["file_id"][0]
            return {"file_id": fid, "versions": list(reversed(self.versions.get(fid, [])))}

        if "/source/versions/" in path and path.endswith("/restore") and method == "POST":
            pid = path.split("/")[3]
            _, root_dir = self.projects[pid]
            ver_id = path.split("/")[-2]
            raw = self.version_bytes[ver_id]
            (root_dir / str(body["path"])).write_bytes(raw)
            return {"version_id": ver_id, "sha256": sha256_bytes(raw), "changed": True}

        raise AssertionError(f"unhandled mock route: {method} {path}")


class TestCF08CorpusAndRunner(unittest.TestCase):
    def test_encode_text_with_bom_all_supported_encodings(self) -> None:
        text = "hello alpha\r\nworld\n"
        for enc in SUPPORTED_REPLACE_ENCODINGS:
            raw = encode_text_with_bom(text, enc)
            self.assertGreater(len(raw), 0)
        self.assertTrue(encode_text_with_bom("a", "utf-8-bom").startswith(b"\xef\xbb\xbf"))
        self.assertTrue(encode_text_with_bom("a", "utf-16-le-bom").startswith(b"\xff\xfe"))
        self.assertTrue(encode_text_with_bom("a", "utf-16-be-bom").startswith(b"\xfe\xff"))

    def test_generators_are_deterministic(self) -> None:
        self.assertEqual(generate_r02_case("20261127", 3), generate_r02_case("20261127", 3))
        self.assertEqual(generate_r03_case("20261127", 7), generate_r03_case("20261127", 7))
        self.assertEqual(generate_r04_case("20261127", 2), generate_r04_case("20261127", 2))

    def test_family_summary_gap_rates(self) -> None:
        s = FamilySummary("R01", "sidecar", "20261127", planned=10, edges_planned=4, executed=10, passed=10, edges_executed=4, edges_passed=4)
        s.record_gap("result_limit")
        d = s.to_dict()
        self.assertEqual(d["declared_gaps"], {"result_limit": 1})
        self.assertAlmostEqual(float(d["declared_gap_rates"]["result_limit"]), round(1 / 14, 6))

    def test_collect_disk_hashes(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            p = Path(tmp) / "a.txt"
            p.write_bytes(b"abc")
            self.assertEqual(collect_disk_hashes(Path(tmp), ["a.txt", "missing.txt"]), {"a.txt": sha256_bytes(b"abc")})

    def test_sidecar_runner_r01_r02_r03_r04_mock_pass(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            runner = CF08SidecarRunner(_FakeSidecarTransport(), Path(tmp), base_seed="20261127")
            s1 = runner.run_r01(count=3)
            self.assertEqual(s1.failed, 0, s1.failures)
            self.assertEqual(s1.edges_failed, 0, s1.failures)
            s2 = runner.run_r02(count=3)
            self.assertEqual(s2.failed, 0, s2.failures)
            self.assertEqual(s2.edges_failed, 0, s2.failures)
            s3 = runner.run_r03(count=4)
            self.assertEqual(s3.failed, 0, s3.failures)
            self.assertEqual(s3.edges_failed, 0, s3.failures)
            s4 = runner.run_r04(count=2)
            self.assertEqual(s4.failed, 0, s4.failures)
            self.assertEqual(s4.edges_failed, 0, s4.failures)

    def test_sidecar_runner_r03_shrinks_defect(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            runner = CF08SidecarRunner(_FakeSidecarTransport(inject_r03_defect=True), Path(tmp), base_seed="20261127")
            s3 = runner.run_r03(count=1)
            self.assertEqual(s3.failed, 1)
            self.assertEqual(len(s3.shrunk_failures), 1)
            self.assertLessEqual(s3.shrunk_failures[0]["shrunk_entry_count"], s3.shrunk_failures[0]["original_entry_count"])


if __name__ == "__main__":
    unittest.main()
