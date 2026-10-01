"""Cache eviction and maintenance admission coverage."""

import importlib.util
import json
import os
from pathlib import Path
import tempfile
import time
import unittest
from unittest.mock import patch

import verification_cache as cache
from verification_resources import Reservation

spec = importlib.util.spec_from_file_location("cache_execution", Path(__file__).resolve().parents[1].joinpath("test-execution.py"))
execution = importlib.util.module_from_spec(spec)
spec.loader.exec_module(execution)


class CacheTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name) / "cache"
        self.root.mkdir()
        self.now = time.time()
        self.sequence = 0

    def entry(self, age, executable=False):
        self.sequence += 1
        path = self.root / "00" / (f"{self.sequence:064x}" + "-d")
        path.parent.mkdir(exist_ok=True)
        if executable:
            path.mkdir()
            (path / "fixture.test").write_bytes(b"x" * 4096)
            os.utime(path / "fixture.test", (self.now - 864000, self.now - 864000))
        else:
            path.write_bytes(b"x" * 4096)
        os.utime(path, (self.now - age, self.now - age))
        return path

    def test_pressure_evicts_oldest_to_low_watermark_without_emptying_cache(self):
        oldest, middle, newest = [self.entry(age) for age in (20000, 10000, 5000)]
        unit = cache.allocated(oldest.stat())
        result = cache.trim(self.root, cache.Budget(2 * unit, unit), now=self.now)
        self.assertFalse(oldest.exists())
        self.assertFalse(middle.exists())
        self.assertTrue(newest.exists())
        self.assertEqual(result["after_bytes"], unit)
        self.assertEqual(result["reclaimed_bytes"], 2 * unit)
        self.assertEqual(result["removed_entries"], 2)

    def test_under_budget_preserves_warm_and_old_compilation_entries(self):
        old = self.entry(864000)
        result = cache.trim(self.root, cache.Budget(), now=self.now)
        self.assertTrue(old.exists())
        self.assertEqual(result["removed_entries"], 0)

    def test_recent_executable_uses_directory_access_time_and_reports_pressure(self):
        recent = self.entry(1800, executable=True)
        old = self.entry(10000, executable=True)
        unit = cache.allocated((recent / "fixture.test").stat())
        result = cache.trim(self.root, cache.Budget(unit // 2, unit // 4), now=self.now)
        self.assertTrue((recent / "fixture.test").exists())
        self.assertFalse(old.exists())
        self.assertTrue(result["over_budget"])

    def test_explicit_age_trim_preserves_recently_used_executable_with_old_binary(self):
        recent = self.entry(1800, executable=True)
        old = self.entry(864000)
        cache.trim(self.root, cache.Budget(), now=self.now, unused_seconds=168 * 3600)
        self.assertTrue((recent / "fixture.test").exists())
        self.assertFalse(old.exists())

    def test_unknown_files_fuzz_inputs_and_symlinks_are_untouched(self):
        external = Path(self.temporary.name) / "external"
        external.write_text("keep")
        entry = self.entry(10000)
        link = entry.with_name("f" * 64 + "-d")
        link.symlink_to(external)
        linked_bucket = self.root / "01"
        linked_bucket.symlink_to(entry.parent, target_is_directory=True)
        for name in ("README", "trim.txt", "fuzz/corpus", "00/unknown"):
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("keep")
        cache.trim(self.root, cache.Budget(1, 1), now=self.now, unused_seconds=0)
        self.assertEqual(external.read_text(), "keep")
        self.assertTrue(link.is_symlink())
        self.assertTrue(linked_bucket.is_symlink())
        for name in ("README", "trim.txt", "fuzz/corpus", "00/unknown"):
            self.assertEqual((self.root / name).read_text(), "keep")

    def test_access_refreshed_during_scan_is_not_evicted(self):
        entry = self.entry(10000)
        captured = list(cache.cache_entries(self.root))
        os.utime(entry, (self.now, self.now))
        with patch.object(cache, "cache_entries", return_value=iter(captured)):
            result = cache.trim(self.root, cache.Budget(1, 1), now=self.now)
        self.assertTrue(entry.exists())
        self.assertEqual(result["removed_entries"], 0)

    def test_scan_cooldown_is_shared_and_budget_changes_force_recheck(self):
        queue = execution.Queue(Path(self.temporary.name) / "queue")
        environment = {"GOCACHE": str(self.root)}
        maintenance = cache.Maintenance(queue, environment)
        with patch.object(cache, "trim", wraps=cache.trim) as trim:
            maintenance.admitted()
            cache.Maintenance(queue, environment).admitted()
            self.assertEqual(trim.call_count, 1)
            cache.Maintenance(queue, {**environment, "GOCACHE_MAX_GIB": "10"}).admitted()
            self.assertEqual(trim.call_count, 2)
        self.assertEqual(len(list(queue.root.glob("go-cache-*.json"))), 2)

    def test_failed_cleanup_has_no_success_stamp_and_can_retry(self):
        queue = execution.Queue(Path(self.temporary.name) / "queue")
        maintenance = cache.Maintenance(queue, {"GOCACHE": str(self.root)})
        with patch.object(cache, "trim", side_effect=OSError("fixture failure")):
            maintenance.admitted()
        self.assertEqual(list(queue.root.glob("go-cache-*.json")), [])
        maintenance.admitted()
        state = json.loads(next(queue.root.glob("go-cache-*.json")).read_text())
        self.assertIn("reclaimed_bytes", state)

    def test_maintenance_waits_for_active_builds_to_drain(self):
        old = self.entry(10000)
        queue = execution.Queue(Path(self.temporary.name) / "queue")
        maintenance = cache.Maintenance(queue, {"GOCACHE": str(self.root)})
        with Reservation(queue, execution.lock_file, "fixture", "Active build",
                         {"locks": ["go"], "shared_locks": ["go"], "workers": 1}, 1) as active:
            self.assertTrue(active.acquire(lambda: True))
            with patch.object(cache.Budget, "environment", return_value=cache.Budget(1, 1)):
                maintenance.scheduled("fixture")
            self.assertTrue(old.exists())
        maintenance.retry_at = 0
        with patch.object(cache.Budget, "environment", return_value=cache.Budget(1, 1)):
            maintenance.scheduled("fixture")
        self.assertFalse(old.exists())

    def test_maintenance_skips_instead_of_waiting_when_not_admissible(self):
        old = self.entry(10000)
        queue = execution.Queue(Path(self.temporary.name) / "queue")
        maintenance = cache.Maintenance(queue, {"GOCACHE": str(self.root)})
        with Reservation(queue, execution.lock_file, "fixture", "Explicit trim", cache.MAINTENANCE, 1) as active:
            self.assertTrue(active.try_acquire())
            with patch.object(cache.Budget, "environment", return_value=cache.Budget(1, 1)):
                started = time.monotonic()
                maintenance.scheduled("fixture")
                self.assertLess(time.monotonic() - started, 2)
                self.assertEqual(len(queue.status()["operations"]), 1)
                self.assertTrue(old.exists())
                self.assertEqual(list(queue.root.glob("go-cache-*.json")), [])
                # The deferral holds until the retry interval without rescanning the cache.
                with patch.object(cache, "cache_entries", side_effect=AssertionError("rescanned")):
                    maintenance.scheduled("fixture")
        maintenance.retry_at = 0
        with patch.object(cache.Budget, "environment", return_value=cache.Budget(1, 1)):
            maintenance.scheduled("fixture")
        self.assertFalse(old.exists())
        self.assertTrue(list(queue.root.glob("go-cache-*.json")))

    def test_maintenance_skips_when_worker_capacity_is_full(self):
        old = self.entry(10000)
        queue = execution.Queue(Path(self.temporary.name) / "queue")
        maintenance = cache.Maintenance(queue, {"GOCACHE": str(self.root)})
        with patch("verification_resources.capacity", return_value=1), \
                Reservation(queue, execution.lock_file, "fixture", "Finished stage", {"locks": [], "workers": 1}, 1) as active:
            self.assertTrue(active.try_acquire())
            with patch.object(cache.Budget, "environment", return_value=cache.Budget(1, 1)):
                maintenance.scheduled("fixture")
        self.assertTrue(old.exists())

    def test_under_budget_measurement_does_not_block_an_active_build(self):
        self.entry(10000)
        queue = execution.Queue(Path(self.temporary.name) / "queue")
        maintenance = cache.Maintenance(queue, {"GOCACHE": str(self.root)})
        with Reservation(queue, execution.lock_file, "fixture", "Active build",
                         {"locks": [], "workers": 1}, 1) as active:
            self.assertTrue(active.acquire(lambda: True))
            with patch.object(cache, "Reservation", side_effect=AssertionError("unexpected reservation")):
                maintenance.scheduled("fixture")
            self.assertTrue(list(queue.root.glob("go-cache-*.json")))

    def test_budget_settings_validate_and_derive_a_smaller_target(self):
        self.assertEqual(cache.Budget.environment({}), cache.Budget())
        self.assertEqual(cache.Budget.environment({"GOCACHE_MAX_GIB": "1"}), cache.Budget(cache.GIB, cache.GIB))
        for values in ({"GOCACHE_MAX_GIB": "0"}, {"GOCACHE_MAX_GIB": "x"},
                       {"GOCACHE_TARGET_GIB": "21"}, {"GOCACHE_TARGET_GIB": "0"}):
            with self.subTest(values=values), self.assertRaises(ValueError):
                cache.Budget.environment(values)


if __name__ == "__main__":
    unittest.main()
