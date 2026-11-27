#!/usr/bin/env python3
"""Negative-control and identity-verification tests for run-paired.sh --mode compare."""

import json
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent
REPO_ROOT = SCRIPT_DIR.parents[2]
RUN_PAIRED = SCRIPT_DIR / "run-paired.sh"


def make_fake_staged_root(root: Path, image_id: str, lib_bytes: bytes) -> None:
    img_dir = root / "portable-python" / "image"
    eng_dir = root / "portable-python" / "engine"
    img_dir.mkdir(parents=True, exist_ok=True)
    eng_dir.mkdir(parents=True, exist_ok=True)
    (img_dir / "image-manifest.json").write_text(
        json.dumps({"image_id": image_id, "cpython_wasm_sha256": "deadbeef"}) + "\n",
        encoding="utf-8",
    )
    (eng_dir / "artifact-manifest.json").write_text(
        json.dumps({"engine_tag": "pw-cpython-wasi-engine-v0.2.0", "source_commit": "a40127b0", "cpython_wasm_sha256": "deadbeef"}) + "\n",
        encoding="utf-8",
    )
    (eng_dir / "libpw_cpython_wasi.so").write_bytes(lib_bytes)


class PairedCompareIdentityControlsTest(unittest.TestCase):
    def test_same_root_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            t = Path(tmp)
            root_a = t / "root-a"
            out_dir = t / "out"
            make_fake_staged_root(root_a, "pp-cpython-3.12.0-wasi-aaa", b"engine-bytes-v1")
            proc = subprocess.run(
                [
                    "bash",
                    str(RUN_PAIRED),
                    "--mode", "compare",
                    "--check-identities-only",
                    "--repo-root", str(REPO_ROOT),
                    "--out-dir", str(out_dir),
                    "--baseline-dir", str(root_a),
                    "--candidate-dir", str(root_a),
                    "--baseline-product-rev", "rev-a",
                    "--candidate-product-rev", "rev-b",
                ],
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(proc.returncode, 0, "same staged root must fail closed")
            self.assertIn("SAME staged engine root", proc.stderr)

    def test_distinct_dirs_with_identical_digests_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            t = Path(tmp)
            root_a = t / "root-a"
            root_b = t / "root-b"
            out_dir = t / "out"
            make_fake_staged_root(root_a, "pp-cpython-3.12.0-wasi-same", b"engine-bytes-v1")
            make_fake_staged_root(root_b, "pp-cpython-3.12.0-wasi-same", b"engine-bytes-v1")
            proc = subprocess.run(
                [
                    "bash",
                    str(RUN_PAIRED),
                    "--mode", "compare",
                    "--check-identities-only",
                    "--repo-root", str(REPO_ROOT),
                    "--out-dir", str(out_dir),
                    "--baseline-dir", str(root_a),
                    "--candidate-dir", str(root_b),
                    "--baseline-product-rev", "rev-a",
                    "--candidate-product-rev", "rev-b",
                ],
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(proc.returncode, 0, "identical image+engine digests must fail closed")
            self.assertIn("IDENTICAL image_manifest_sha256", proc.stderr)

    def test_distinct_images_pass_and_record_separate_revisions(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            t = Path(tmp)
            root_a = t / "root-a"
            root_b = t / "root-b"
            out_dir = t / "out"
            make_fake_staged_root(root_a, "pp-cpython-3.12.0-wasi-py10", b"engine-bytes-v1")
            make_fake_staged_root(root_b, "pp-cpython-3.12.0-wasi-py11", b"engine-bytes-v1")
            proc = subprocess.run(
                [
                    "bash",
                    str(RUN_PAIRED),
                    "--mode", "compare",
                    "--check-identities-only",
                    "--repo-root", str(REPO_ROOT),
                    "--out-dir", str(out_dir),
                    "--baseline-dir", str(root_a),
                    "--candidate-dir", str(root_b),
                    "--baseline-product-rev", "ed905e29c7",
                    "--candidate-product-rev", "1111222233",
                ],
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertIn("COMPARE_IDENTITIES_OK", proc.stdout)
            idents = json.loads((out_dir / "compare-identities.json").read_text(encoding="utf-8"))
            self.assertEqual(idents["baseline"]["product_revision"], "ed905e29c7")
            self.assertEqual(idents["candidate"]["product_revision"], "1111222233")
            self.assertEqual(idents["baseline"]["image_id"], "pp-cpython-3.12.0-wasi-py10")
            self.assertEqual(idents["candidate"]["image_id"], "pp-cpython-3.12.0-wasi-py11")
            self.assertFalse(idents["same_root"])
            self.assertFalse(idents["same_image_and_engine_digest"])


if __name__ == "__main__":
    unittest.main()
