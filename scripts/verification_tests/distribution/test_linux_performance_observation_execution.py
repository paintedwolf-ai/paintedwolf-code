"""Existing sidecar budgets remain evidence rather than full native qualification."""

import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from linux_release.performance_observation import observe
from linux_release.support_matrix import CATALOG, expand

SOURCE = "a" * 40


class PerformanceObservationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.catalog = json.loads(CATALOG.read_text())
        self.row = next(row for row in expand(self.catalog)
                        if row["capability"] == "performance" and row["architecture"] == "amd64")
        artifact = {"id": "final", "installation": self.row["installation"], "architecture": "amd64",
                    "source_commit": SOURCE, "identity": {"kind": "file", "sha256": "b" * 64}}
        self.final = {"format_version": 1, "source_commit": SOURCE, "artifacts": [artifact]}
        self.report = {"version": 1, "iterations": 20, "metrics": {"startup.ready": {"p95_ms": 50}},
                       "resources": {"samples": 10}, "correctness": {"sqlite_quick_check": "ok"},
                       "budgets": {"latency_p95_ms": {"startup.ready": 7500}}, "violations": None}

    def observation(self, **changes):
        data = json.dumps(self.report).encode()
        (self.root / "perf.json").write_bytes(data)
        args = dict(catalog=self.catalog, final=self.final, source_commit=SOURCE, artifact_id="final",
                    row_id=self.row["id"], report_file={"path": "perf.json", "sha256": hashlib.sha256(data).hexdigest()},
                    root=self.root, facts={"architecture": "amd64"}, execution="native")
        args.update(changes)
        return observe(**args)

    def test_passing_sidecar_report_preserves_identity_and_unqualified_cases(self):
        result = self.observation()
        self.assertEqual(result["reported_budget_status"], "passed")
        self.assertEqual(result["artifact"], self.final["artifacts"][0])
        self.assertEqual(result["qualification_status"], "unverified")
        self.assertEqual(result["remaining_cases"], self.row["cases"])
        self.assertNotIn("cases", result)

    def test_existing_budget_violation_is_reported_without_prose_policy(self):
        self.report["violations"] = ["fixture budget exceeded"]
        result = self.observation()
        self.assertEqual(result["reported_budget_status"], "failed")
        self.assertEqual(result["qualification_status"], "unverified")
        self.assertEqual(result["reported_violations"], self.report["violations"])

    def test_emulation_never_becomes_native_qualification(self):
        result = self.observation(execution="emulated")
        self.assertEqual(result["execution"], "emulated")
        self.assertEqual(result["qualification_status"], "unverified")
        with self.assertRaises(ValueError):
            self.observation(execution="unknown")

    def test_source_artifact_row_and_architecture_must_agree(self):
        for changes in ({"source_commit": "c" * 40}, {"artifact_id": "missing"},
                        {"row_id": "unknown"}, {"facts": {"architecture": "arm64"}}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                self.observation(**changes)
        self.final["artifacts"][0]["installation"] = "other"
        with self.assertRaises(ValueError):
            self.observation()

    def test_report_must_be_retained_and_unchanged(self):
        for record in ({"path": "../outside", "sha256": "b" * 64},
                       {"path": "perf.json", "sha256": "b" * 64}):
            with self.subTest(record=record), self.assertRaises(ValueError):
                self.observation(report_file=record)

    def test_missing_malformed_or_unsupported_reports_refuse(self):
        original = copy.deepcopy(self.report)
        for key, value in (("version", True), ("version", 2), ("metrics", {}),
                           ("resources", []), ("iterations", 0), ("violations", [1])):
            self.report = {**copy.deepcopy(original), key: value}
            with self.subTest(key=key, value=value), self.assertRaises(ValueError):
                self.observation()
        self.report = copy.deepcopy(original)
        del self.report["violations"]
        with self.assertRaises(ValueError):
            self.observation()
