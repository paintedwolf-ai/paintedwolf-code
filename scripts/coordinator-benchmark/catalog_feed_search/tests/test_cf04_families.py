"""Unit tests for CF04 family modules (K05, K06, F07) without live sidecars."""

from __future__ import annotations

import hashlib
from pathlib import Path
import tempfile
import unittest

from catalog_feed_search.families.cf04_common import (
    branch_dir_for_root_id,
    count_workspace_live_watchers,
    sha256_bytes,
    write_sha256sums,
)
from catalog_feed_search.families.cf04_f07 import generate_f07_script
from catalog_feed_search.families.cf04_k05 import (
    FILES_PER_DIRECTORY,
    scalefixture_body,
    scalefixture_path,
    write_scalefixture_tree,
)
from catalog_feed_search.families.cf04_k06 import classify_retired_worker_response, generate_k06_spec
from catalog_feed_search.families.cf04_k05_trace import parse_strace_fixture_ops
from catalog_feed_search.seeds import BASE_SEED_PRIMARY


class TestCF04GeneratorsAndHelpers(unittest.TestCase):
    def test_branch_dir_for_root_id_matches_go_sha256(self) -> None:
        rid = "root-abc"
        want = "root-" + hashlib.sha256(rid.encode("utf-8")).hexdigest()
        self.assertEqual(branch_dir_for_root_id(rid), want)
        with self.assertRaises(ValueError):
            branch_dir_for_root_id("   ")

    def test_count_workspace_live_watchers(self) -> None:
        ws = {
            "roots": [
                {"id": "r1", "watch": {"state": "live"}},
                {"id": "r2", "watch": {"state": "partial"}},
                {"id": "r3", "watch": {"state": "faulted"}},
                {"id": "r4", "watch": {"state": "unwatched"}},
            ]
        }
        summary = count_workspace_live_watchers(ws)
        self.assertEqual(summary["root_count"], 4)
        self.assertEqual(summary["live_or_partial_count"], 2)
        self.assertEqual(summary["faulted_count"], 1)
        self.assertEqual(summary["unwatched_count"], 1)

    def test_k06_spec_deterministic_and_within_ledger_ranges(self) -> None:
        s1 = generate_k06_spec(BASE_SEED_PRIMARY, 0)
        s2 = generate_k06_spec(BASE_SEED_PRIMARY, 0)
        self.assertEqual(s1, s2)
        for i in range(100):
            spec = generate_k06_spec(BASE_SEED_PRIMARY, i)
            self.assertGreaterEqual(spec.root_count, 1)
            self.assertLessEqual(spec.root_count, 4)
            self.assertGreaterEqual(spec.worker_count, 0)
            self.assertLessEqual(spec.worker_count, 2)

    def test_f07_script_deterministic_and_within_ledger_ranges(self) -> None:
        s1 = generate_f07_script(BASE_SEED_PRIMARY, 7)
        s2 = generate_f07_script(BASE_SEED_PRIMARY, 7)
        self.assertEqual(s1, s2)
        for i in range(40):
            spec = generate_f07_script(BASE_SEED_PRIMARY, i)
            self.assertGreaterEqual(len(spec.steps), 1)
            self.assertLessEqual(len(spec.steps), 6)
            self.assertEqual(spec.steps[0], "attach")

    def test_scalefixture_exact_parity_with_go(self) -> None:
        self.assertEqual(scalefixture_path(0), "g000/d000/f000.go")
        self.assertEqual(scalefixture_path(1), "g000/d000/f001.ts")
        self.assertEqual(scalefixture_path(FILES_PER_DIRECTORY), "g000/d001/f000.go")
        self.assertEqual(scalefixture_path(FILES_PER_DIRECTORY * 50), "g001/d000/f000.go")
        b0 = scalefixture_body(0)
        self.assertEqual(len(b0), 128)
        self.assertTrue(b0.startswith(b"// scale fixture file 0\n"))
        self.assertTrue(b0.endswith(b"\n"))

        with tempfile.TemporaryDirectory() as tmp:
            shape = write_scalefixture_tree(Path(tmp) / "tree", 2 * FILES_PER_DIRECTORY + 7)
            self.assertEqual(shape.files, 1007)
            self.assertEqual(shape.leaf_directories, 3)
            self.assertEqual(shape.group_directories, 1)
            self.assertEqual(shape.total_directories, 5)
            self.assertTrue((Path(tmp) / "tree" / "g000/d002/f006.ts").is_file())

    def test_write_sha256sums(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "a.txt").write_bytes(b"alpha")
            (root / "sub").mkdir()
            (root / "sub" / "b.txt").write_bytes(b"beta")
            sums = write_sha256sums(root)
            lines = sums.read_text(encoding="utf-8").strip().splitlines()
            self.assertEqual(len(lines), 2)
            self.assertIn(f"{sha256_bytes(b'alpha')}  a.txt", lines)
            self.assertIn(f"{sha256_bytes(b'beta')}  sub/b.txt", lines)

    def test_classify_retired_worker_response_positive_and_negative_controls(self) -> None:
        trunk_bytes = b"trunk content\n"
        trunk_sha = sha256_bytes(trunk_bytes)

        # Positive control 1: conforming raw D1 project-root fallback
        kind, rec = classify_retired_worker_response(
            retired_worker_id="w-1",
            expected_root_id="r-1",
            expected_trunk_sha256=trunk_sha,
            status=200,
            json_body={
                "workspace_kind": "project",
                "root_id": "r-1",
                "sha256": trunk_sha,
                "content": trunk_bytes.decode("utf-8"),
            },
            body_text="{}",
        )
        self.assertEqual(kind, "observation")
        self.assertEqual(rec["code"], "retired_worker_raw_api_project_fallback")

        # Positive control 2: explicit documented unavailable response
        kind, rec = classify_retired_worker_response(
            retired_worker_id="w-1",
            expected_root_id="r-1",
            expected_trunk_sha256=trunk_sha,
            status=410,
            json_body={"code": "worker_workspace_unavailable", "message": "retired"},
            body_text="{}",
        )
        self.assertEqual(kind, "observation")
        self.assertEqual(rec["code"], "retired_worker_explicit_unavailable")

        # Negative control 1: HTTP 200 with mismatched content digest
        kind, rec = classify_retired_worker_response(
            retired_worker_id="w-1",
            expected_root_id="r-1",
            expected_trunk_sha256=trunk_sha,
            status=200,
            json_body={
                "workspace_kind": "project",
                "root_id": "r-1",
                "sha256": trunk_sha,
                "content": "corrupted body\n",
            },
            body_text="{}",
        )
        self.assertEqual(kind, "defect")
        self.assertEqual(rec["code"], "retired_worker_invalid_200_response")

        # Negative control 2: HTTP 200 falsely claiming workspace_kind="worker"
        kind, rec = classify_retired_worker_response(
            retired_worker_id="w-1",
            expected_root_id="r-1",
            expected_trunk_sha256=trunk_sha,
            status=200,
            json_body={
                "workspace_kind": "worker",
                "root_id": "r-1",
                "sha256": trunk_sha,
                "content": trunk_bytes.decode("utf-8"),
            },
            body_text="{}",
        )
        self.assertEqual(kind, "defect")
        self.assertEqual(rec["code"], "retired_worker_invalid_200_response")

        # Negative control 3: malformed non-JSON body or unexpected HTTP 500
        kind, rec = classify_retired_worker_response(
            retired_worker_id="w-1",
            expected_root_id="r-1",
            expected_trunk_sha256=trunk_sha,
            status=200,
            json_body=None,
            body_text="not-json",
        )
        self.assertEqual(kind, "defect")
        self.assertEqual(rec["code"], "retired_worker_malformed_response")

        kind, rec = classify_retired_worker_response(
            retired_worker_id="w-1",
            expected_root_id="r-1",
            expected_trunk_sha256=trunk_sha,
            status=500,
            json_body={"code": "internal_error"},
            body_text='{"code":"internal_error"}',
        )
        self.assertEqual(kind, "defect")
        self.assertEqual(rec["code"], "retired_worker_unexpected_http_error")

    def test_parse_strace_fixture_ops_positive_and_negative_controls(self) -> None:
        root = Path("/tmp/fixroot")
        sample = "\n".join(
            [
                '101 getdents64(5</tmp/fixroot>, 0x1, 8192) = 128',
                '101 getdents64(5</tmp/fixroot>, 0x1, 8192) = 0',
                '101 getdents64(6</tmp/fixroot/sub>, 0x1, 8192) = 64',
                '101 getdents64(7</etc>, 0x1, 8192) = 512',
                '101 newfstatat(6</tmp/fixroot/sub>, "a.go", {st_mode=S_IFREG}, 0) = 0',
                '101 newfstatat(AT_FDCWD, "/tmp/fixroot/sub", {st_mode=S_IFDIR}, 0) = 0',
            ]
        )
        obs = parse_strace_fixture_ops(sample, root, known_dir_rels={".", "sub"})
        self.assertEqual(obs["status"], "ok")
        self.assertEqual(obs["distinct_directories_read"], 2)
        self.assertEqual(obs["directory_read_calls"], 2)
        self.assertEqual(obs["per_file_metadata_reads"], 1)
        self.assertEqual(obs["directory_metadata_reads"], 1)

        empty = parse_strace_fixture_ops("", root, known_dir_rels={"."})
        self.assertEqual(empty["status"], "unknown")


if __name__ == "__main__":
    unittest.main()
