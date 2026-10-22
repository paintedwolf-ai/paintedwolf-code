"""Cache admission reserves space without touching unknown families or branch caches."""
from pathlib import Path
import tempfile
import unittest

from ci_policy import cache_budget as budget


def row(identifier, key, size, ref="refs/heads/main"):
    return dict(id=identifier, key=key, size_in_bytes=size, ref=ref, last_accessed_at=str(identifier))


class CacheBudgetTests(unittest.TestCase):
    def test_generations_replace_only_the_same_platform_family(self):
        rows = [row(1, "go-Linux-X64-old", 2), row(2, "go-macOS-ARM64-old", 2),
                row(3, "go-Linux-X64-branch", 2, "refs/pull/1/merge"), row(4, "unknown", 2)]
        result = budget.plan(rows, "go-Linux-X64-new", 10, limit=2**22)
        self.assertTrue(result["allowed"])
        self.assertEqual(result["delete"], [1])
        self.assertEqual(budget.plan(rows, "go-Linux-X64-old", 10)["delete"], [])

    def test_budget_counts_unowned_storage_and_refuses_oversized_writes(self):
        rows = [row(1, "go-Linux-X64-old", 50), row(2, "unknown", 2**22)]
        result = budget.plan(rows, "go-Linux-X64-new", 10, limit=2**22)
        self.assertFalse(result["allowed"])
        self.assertEqual(result["delete"], [])
        self.assertEqual(budget.plan(rows, "go-Linux-X64-new", 0)["delete"], [])
        with self.assertRaises(ValueError):
            budget.plan(rows, "unowned-new", 10)

    def test_space_is_reserved_before_retiring_other_owned_entries(self):
        rows = [row(1, "go-Linux-X64-old", 2**20), row(2, "decide-v2-macOS-ARM64-old", 2**20),
                row(3, "unknown", 100)]
        result = budget.plan(rows, "go-Linux-X64-new", 2**20, limit=3 * 2**20)
        self.assertTrue(result["allowed"])
        self.assertEqual(result["delete"], [1, 2])
        self.assertLessEqual(result["projected_bytes"], result["limit_bytes"])

    def test_payload_measurement_obeys_exclusions_and_ignores_symlinks(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "keep").write_bytes(b"a")
            (root / "incremental").mkdir()
            (root / "incremental" / "skip").write_bytes(b"b")
            (root / "link").symlink_to(root / "keep")
            self.assertEqual(budget.files(f"{root}\n!{root}/**/incremental"), {root / "keep"})

    def test_go_trimming_removes_only_content_addressed_entries(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / ("a" * 64 + "-d")).write_bytes(b"12345")
            (root / "README").write_text("keep")
            self.assertEqual(budget.trim_go(root, 4), 4)
            self.assertEqual([p.name for p in root.iterdir()], ["README"])
