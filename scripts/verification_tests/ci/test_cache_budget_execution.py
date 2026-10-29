"""Cache admission reserves space without touching unknown families or branch caches."""
import json
import os
from unittest.mock import patch
from pathlib import Path
import tempfile
import unittest

from ci_policy import cache_budget as budget


def row(identifier, key, size, ref="refs/heads/main"):
    return dict(id=identifier, key=key, size_in_bytes=size, ref=ref, last_accessed_at=str(identifier))


class CacheBudgetTests(unittest.TestCase):
    def test_document_compiler_family_shares_the_existing_storage_budget(self):
        self.assertEqual(budget.family("document-core-v1-macOS-ARM64-tools"), ("document-core", "macOS", "ARM64"))
        rows = [row(1, "document-core-v1-macOS-ARM64-old", 10), row(2, "document-core-v1-Linux-X64-old", 10)]
        planned = budget.plan(rows, "document-core-v1-macOS-ARM64-new", 10)
        self.assertEqual(planned["retire_after_save"], [1])
        self.assertEqual(planned["limit_bytes"], 8 * 1024**3)

    def test_generations_replace_only_the_same_platform_family(self):
        rows = [row(1, "go-Linux-X64-old", 2), row(2, "go-macOS-ARM64-old", 2),
                row(3, "go-Linux-X64-branch", 2, "refs/pull/1/merge"), row(4, "unknown", 2)]
        result = budget.plan(rows, "go-Linux-X64-new", 10, limit=2**22)
        self.assertTrue(result["allowed"])
        self.assertEqual(result["delete"], [])
        self.assertEqual(result["retire_after_save"], [1])
        self.assertEqual(budget.plan(rows, "go-Linux-X64-old", 10)["delete"], [])

    def test_budget_counts_unowned_storage_and_refuses_oversized_writes(self):
        rows = [row(1, "go-Linux-X64-old", 50), row(2, "unknown", 2**22)]
        result = budget.plan(rows, "go-Linux-X64-new", 10, limit=2**22)
        self.assertFalse(result["allowed"])
        self.assertEqual(result["delete"], [])
        self.assertEqual(budget.plan(rows, "go-Linux-X64-new", 0)["delete"], [])
        with self.assertRaises(ValueError):
            budget.plan(rows, "unowned-new", 10)

    def test_storage_pressure_cannot_displace_another_compiler_family(self):
        rows = [row(1, "go-Linux-X64-old", 2**20), row(2, "decide-v2-macOS-ARM64-old", 2**20),
                row(3, "unknown", 100)]
        result = budget.plan(rows, "go-Linux-X64-new", 2**20, limit=3 * 2**20)
        self.assertFalse(result["allowed"])
        self.assertEqual(result["delete"], [])
        self.assertGreater(result["projected_bytes"], result["limit_bytes"])

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

    def test_prior_generation_is_retired_only_after_inventory_confirms_the_new_payload(self):
        old = row(1, "go-Linux-X64-old", 2**20)
        record = {**budget.plan([old], "go-Linux-X64-new", 10), "key": "go-Linux-X64-new",
                  "measured_compressed_bytes": 10}
        self.assertEqual(record["delete"], [])
        for inventory in ([old], [old, row(2, record["key"], 10, "refs/pull/1/merge")],
                          [old, row(2, record["key"], 0)]):
            result = budget.confirm(inventory, record)
            self.assertFalse(result["published"])
            self.assertEqual(result["delete"], [])
        saved = row(2, record["key"], 12)
        result = budget.confirm([old, saved], record)
        self.assertTrue(result["published"])
        self.assertEqual(result["delete"], [1])
        self.assertEqual(result["stored_compressed_bytes"], 12)
        self.assertEqual(result["measured_compressed_bytes"], 10)
        changed_owner = {**old, "ref": "refs/pull/1/merge"}
        self.assertEqual(budget.confirm([changed_owner, saved], record)["delete"], [])
        self.assertEqual(budget.confirm([old, saved, {**saved, "id": 3}], record)["delete"], [])

    def test_capacity_pressure_deletes_only_the_space_needed_before_save(self):
        rows = [row(1, "go-Linux-X64-old", 2**20), row(2, "go-Linux-X64-older", 2**20)]
        result = budget.plan(rows, "go-Linux-X64-new", 10, limit=3 * 2**20)
        self.assertEqual(result["delete"], [1])
        self.assertEqual(result["retire_after_save"], [2])

    def test_publication_confirmation_deletes_only_confirmed_prior_generation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            key = "go-Linux-X64-new"
            record = root / "record.json"
            record.write_text(json.dumps({"key": key, "allowed": True, "retire_after_save": [1],
                                          "measured_compressed_bytes": 10, "reserved_bytes": 100}))
            env = {"GITHUB_REF": "refs/heads/main", "GITHUB_ACTIONS": "true",
                   "RUNNER_ENVIRONMENT": "github-hosted", "CACHE_KEY": key,
                   "GITHUB_REPOSITORY": "owner/repo", "CACHE_RECORD": str(record),
                   "GITHUB_STEP_SUMMARY": str(root / "summary")}
            inventory = [row(1, "go-Linux-X64-old", 20), row(2, key, 12)]
            with patch.dict(os.environ, env, clear=True), patch.object(budget, "pages", return_value=inventory), \
                    patch.object(budget, "api") as api:
                budget.main()
            api.assert_called_once_with("repos/owner/repo/actions/caches/1", "DELETE")
            self.assertIn('"stored_compressed_bytes": 12', (root / "summary").read_text())
            with patch.dict(os.environ, {**env, "GITHUB_REF": "refs/pull/1/merge"}, clear=True), \
                    self.assertRaises(ValueError):
                budget.main()


class GoCachePayloadTests(unittest.TestCase):
    def test_go_payload_keeps_compiler_outputs_without_module_archives(self):
        import yaml
        root = Path(__file__).resolve().parents[3]
        action = yaml.safe_load((root / ".github/actions/cache-go/action.yml").read_text())
        payloads = [step["with"]["path"] for step in action["runs"]["steps"]
                    if "path" in step.get("with", {})]
        self.assertEqual(len(payloads), 3)
        self.assertTrue(all(path.strip() == "${{ steps.paths.outputs.gocache }}" for path in payloads))
        provision = (root / "scripts/ci_policy/provision.sh").read_text()
        self.assertIn('go_retry.py" "$ROOT/lycaon"', provision)
        retry = (root / "scripts/ci_policy/go_retry.py").read_text()
        self.assertIn('["go", "mod", "download"]', retry)
