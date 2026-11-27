"""Memory warning line (80% of ceiling) and bounded review records in ./task budgets."""
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import budgets
from ci_policy import memory_budgets as mb


def _write_receipt(artifact_dir, batch, name, tree_sha, stage, results, status="passed", finished_at=100.0):
    path = Path(artifact_dir) / "verification" / batch / name
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps({
        "status": status, "source_tree": tree_sha, "finished_at": finished_at,
        "evidence": [{"stage": stage, "results": results}],
    }))


def _obs(peak_bytes, limit_bytes, source="proc_vmhwm", lower_bound=True):
    ratio = round(peak_bytes / limit_bytes, 4) if peak_bytes is not None else None
    return {"status": "passed", "memory": {
        "peak_bytes": peak_bytes, "limit_bytes": limit_bytes, "ratio": ratio,
        "source": source, "lower_bound": lower_bound,
    }}


def _review(package, lane, ceiling_bytes, boundaries, disposition="retain"):
    return {
        "package": package, "lane": lane, "workstream": "test-memory-headroom",
        "disposition": disposition, "reason": "Shared fixture requires bounded headroom.",
        "reviewed_commit": "a" * 40, "ceiling_bytes": ceiling_bytes, "boundaries": boundaries,
    }


class MemoryBudgetTests(unittest.TestCase):
    def test_exact_threshold_compares_bytes_before_display_rounding(self):
        limit = 1000 * mb.MIB
        just_under = limit * 80 // 100 - 1  # 79.9999999% rounds to 80.0% for display
        exact = limit * 80 // 100
        above = limit * 81 // 100
        self.assertFalse(mb.at_or_above_warning(just_under, limit))
        self.assertTrue(mb.at_or_above_warning(exact, limit))
        self.assertTrue(mb.at_or_above_warning(above, limit))
        self.assertFalse(mb.at_or_above_warning(None, limit))

    def test_budgets_flags_at_and_above_warning_and_accepts_under_and_retained_records(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            repo = root / "repo"
            artifacts = root / "artifacts"
            reviews = root / "reviews"
            (repo / "lycaon/test/contract/retained").mkdir(parents=True)
            contract = repo / "lycaon/test/contract/retained/contract_test.go"
            contract.write_text("package retained\n")
            digest = hashlib.sha256(contract.read_bytes()).hexdigest()
            reviews.mkdir()
            tree = "b" * 40
            ordinary_limit = 3584 * mb.MIB
            race_limit = int(ordinary_limit * 2.5)

            # Older receipt on same tree should be superseded by newer receipt.
            _write_receipt(artifacts, "b0", "1.json", tree, "test:contract", {
                "example/under": _obs(int(ordinary_limit * 0.90), ordinary_limit),
            }, finished_at=50.0)
            # Receipt for a different tree or non-passing status must be ignored.
            _write_receipt(artifacts, "b-other", "1.json", "c" * 40, "test:contract", {
                "example/other-tree": _obs(int(ordinary_limit * 0.95), ordinary_limit),
            }, finished_at=200.0)
            _write_receipt(artifacts, "b-fail", "1.json", tree, "test:contract", {
                "example/failed-run": _obs(int(ordinary_limit * 0.95), ordinary_limit),
            }, status="failed", finished_at=200.0)
            # Progress file must be skipped.
            (artifacts / "verification" / "b1").mkdir(parents=True)
            (artifacts / "verification" / "b1" / "1.progress.json").write_text("{}")

            # Latest qualifying ordinary and race receipts for `tree`.
            exact_threshold = (ordinary_limit * 80 + 99) // 100
            _write_receipt(artifacts, "b1", "1.json", tree, "test:contract", {
                "example/under": _obs(exact_threshold - 1, ordinary_limit),
                "example/exact": _obs(exact_threshold, ordinary_limit),
                "example/above81": _obs(int(ordinary_limit * 0.81), ordinary_limit, source="proc_vmrss", lower_bound=True),
                "example/retained": _obs(int(ordinary_limit * 0.85), ordinary_limit),
                "example/stale-ceiling": _obs(int(ordinary_limit * 0.92), ordinary_limit),
                "example/stale-boundary": _obs(int(ordinary_limit * 0.84), ordinary_limit),
                "example/refactor": _obs(int(ordinary_limit * 0.83), ordinary_limit),
                "example/missing": _obs(None, ordinary_limit, source="unavailable", lower_bound=False),
            }, finished_at=100.0)
            _write_receipt(artifacts, "b2", "1.json", tree, "test:race", {
                "example/race-hot": _obs(int(race_limit * 0.82), race_limit),
            }, finished_at=101.0)

            rel_contract = "lycaon/test/contract/retained/contract_test.go"
            (reviews / "retained.json").write_text(json.dumps(
                _review("example/retained", "ordinary", int(ordinary_limit * 0.90), {rel_contract: digest})))
            (reviews / "stale-ceiling.json").write_text(json.dumps(
                _review("example/stale-ceiling", "ordinary", int(ordinary_limit * 0.88), {rel_contract: digest})))
            (reviews / "stale-boundary.json").write_text(json.dumps(
                _review("example/stale-boundary", "ordinary", int(ordinary_limit * 0.90), {rel_contract: "0" * 64})))
            (reviews / "refactor.json").write_text(json.dumps(
                _review("example/refactor", "ordinary", 0, {}, disposition="refactor")))

            found = mb.findings(repo, artifacts, tree, reviews_dir=reviews)
            by_text = {(f.level, f.text) for f in found}
            self.assertTrue(all(f.area == "memory" for f in found))
            self.assertFalse(any(f.level == "error" for f in found))
            self.assertFalse(any("example/under" in f.text for f in found))
            self.assertFalse(any("example/other-tree" in f.text for f in found))
            self.assertFalse(any("example/failed-run" in f.text for f in found))

            exact_f = next(f for f in found if "example/exact" in f.text)
            self.assertEqual(exact_f.level, "warning")
            self.assertIn("80.0%", exact_f.text)
            self.assertIn("proc_vmhwm lower bound", exact_f.text)

            above_f = next(f for f in found if "example/above81" in f.text)
            self.assertEqual(above_f.level, "warning")
            self.assertIn("81.0%", above_f.text)
            self.assertIn("proc_vmrss lower bound", above_f.text)

            retained_f = next(f for f in found if "example/retained" in f.text)
            self.assertEqual(retained_f.level, "notice")
            self.assertIn("design decision retain (retained", retained_f.text)

            ceiling_f = next(f for f in found if "example/stale-ceiling" in f.text)
            self.assertEqual(ceiling_f.level, "warning")
            self.assertIn("ceiling_exceeded", ceiling_f.text)

            boundary_f = next(f for f in found if "example/stale-boundary" in f.text)
            self.assertEqual(boundary_f.level, "warning")
            self.assertIn("boundary_changed", boundary_f.text)

            refactor_f = next(f for f in found if "example/refactor" in f.text)
            self.assertEqual(refactor_f.level, "warning")
            self.assertIn("(refactor)", refactor_f.text)

            race_f = next(f for f in found if "example/race-hot" in f.text)
            self.assertEqual(race_f.level, "warning")
            self.assertIn("(race)", race_f.text)

            unavail_f = next(f for f in found if "unavailable memory observations" in f.text)
            self.assertEqual(unavail_f.level, "info")
            self.assertIn("example/missing", unavail_f.text)
            self.assertTrue(by_text)

    def test_invalid_and_duplicate_review_records_warn_without_blocking(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            artifacts = root / "artifacts"
            reviews = root / "reviews"
            reviews.mkdir()
            tree = "d" * 40
            limit = 1000 * mb.MIB
            _write_receipt(artifacts, "b1", "1.json", tree, "test:short", {
                "example/bad": _obs(int(limit * 0.85), limit),
            })
            (reviews / "01-malformed.json").write_text("{not json")
            (reviews / "02-missing.json").write_text(json.dumps({"package": "x"}))
            (reviews / "03-over-limit.json").write_text(json.dumps(
                _review("example/bad", "ordinary", limit + 1, {"a.go": "f" * 64})))
            (reviews / "04-dup.json").write_text(json.dumps(
                _review("example/bad", "ordinary", int(limit * 0.90), {"a.go": "f" * 64})))
            found = mb.findings(root, artifacts, tree, reviews_dir=reviews)
            self.assertTrue(all(f.level == "warning" for f in found))
            self.assertTrue(any("malformed JSON" in f.text for f in found))
            self.assertTrue(any("missing package or lane" in f.text for f in found))
            self.assertTrue(any("duplicate review record" in f.text for f in found))
            self.assertTrue(any("invalid review record 03-over-limit.json" in f.text for f in found))
            obs = {"peak_bytes": int(limit * 0.85), "limit_bytes": limit}
            bad_cases = [
                {**_review("p", "ordinary", int(limit * 0.9), {"a.go": "f" * 64}), "extra": 1},
                _review("", "ordinary", int(limit * 0.9), {"a.go": "f" * 64}),
                _review("p", "other", int(limit * 0.9), {"a.go": "f" * 64}),
                {**_review("p", "ordinary", int(limit * 0.9), {"a.go": "f" * 64}), "workstream": "BAD"},
                _review("p", "ordinary", int(limit * 0.9), {"a.go": "f" * 64}, disposition="ignore"),
                {**_review("p", "ordinary", int(limit * 0.9), {"a.go": "f" * 64}), "reason": ""},
                {**_review("p", "ordinary", int(limit * 0.9), {"a.go": "f" * 64}), "ceiling_bytes": "900"},
                _review("p", "ordinary", 10, {}, disposition="refactor"),
                _review("p", "ordinary", int(limit * 0.80), {"a.go": "f" * 64}),
                _review("p", "ordinary", int(limit * 0.90), {}),
                _review("p", "ordinary", int(limit * 0.90), {"../escape": "f" * 64}),
            ]
            for case in bad_cases:
                with self.subTest(case=case), self.assertRaises(ValueError):
                    mb.evaluate_review(case, obs, root)
            # Missing boundary file marks retained review as boundary_changed; race lane inferred from scaled ceiling.
            self.assertEqual(
                mb.evaluate_review(_review("p", "ordinary", int(limit * 0.90), {"missing.go": "f" * 64}), obs, root),
                "boundary_changed",
            )
            self.assertEqual(mb._lane_for("test:digest", "example/p", int(3584 * mb.MIB * 2.5)), "race")

    def test_empty_receipts_returns_no_findings_and_budgets_main_never_blocks_on_memory_warning(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.assertEqual(mb.findings(root, root / "missing", "e" * 40, reviews_dir=root / "none"), [])
            scope = type("Scope", (), {"base": "1" * 40, "paths": [], "describe": lambda self: "base"})()
            empty_report = {"categories": {}, "findings": [], "untouched": {}, "warnings": [], "notes": [],
                            "tracking": {"schema_version": 1, "complete": True, "artifacts": []}}
            fake_git = type("Res", (), {"stdout": "2" * 40 + "\n"})()
            with patch.object(budgets.change_report, "resolve", return_value=scope), \
                    patch.object(budgets, "change_set", return_value={}), \
                    patch.object(budgets, "artifact_root", return_value=root), \
                    patch.object(budgets, "run_suites", return_value=(0, {"maintainability": empty_report, "prompts": empty_report})), \
                    patch.object(budgets.change_report, "git", return_value=fake_git), \
                    patch.object(budgets.memory_budgets, "findings", return_value=[
                        budgets.Finding("warning", "memory", "example/hot (ordinary): 81.0%")]), \
                    patch.object(budgets.change_report, "emit") as emit:
                (root / "budgets" / "reports").mkdir(parents=True)
                self.assertEqual(budgets.main(), 0)
            emitted_findings, emitted_failed = emit.call_args.args[3], emit.call_args.args[4]
            self.assertFalse(emitted_failed)
            self.assertTrue(any(f.area == "memory" and f.level == "warning" for f in emitted_findings))


if __name__ == "__main__":
    unittest.main()
