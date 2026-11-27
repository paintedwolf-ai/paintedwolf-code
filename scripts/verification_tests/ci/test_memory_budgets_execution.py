"""Memory warning line (80% of ceiling), bounded review records, and post-merge reconciliation."""
import hashlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import zipfile

import budgets
from ci_policy import budget_evidence as evidence, budget_snapshot as snapshot
from ci_policy import maintainability_issues as tracker, memory_budgets as mb


def _write_receipt(artifact_dir, batch, name, tree_sha, stage, results, status="passed", finished_at=100.0,
                   source_manifest_tree=None):
    batch_dir = Path(artifact_dir) / "verification" / batch
    batch_dir.mkdir(parents=True, exist_ok=True)
    if source_manifest_tree is not None:
        (batch_dir / "source.json").write_text(json.dumps({"source_tree": source_manifest_tree}))
    (batch_dir / name).write_text(json.dumps({
        "status": status, "source_tree": tree_sha, "finished_at": finished_at, "names": [stage],
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


def _zip_bytes(entries):
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as archive:
        for name, value in entries.items():
            archive.writestr(name, json.dumps(value) if not isinstance(value, str) else value)
    return buf.getvalue()


class MemoryBudgetTests(unittest.TestCase):
    def test_exact_threshold_compares_bytes_before_display_rounding(self):
        limit = 1000 * mb.MIB
        just_under = limit * 80 // 100 - 1
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

            _write_receipt(artifacts, "b0", "1.json", tree, "test:contract", {
                "example/under": _obs(int(ordinary_limit * 0.90), ordinary_limit),
            }, finished_at=50.0, source_manifest_tree=tree)
            _write_receipt(artifacts, "b-other", "1.json", "c" * 40, "test:contract", {
                "example/other-tree": _obs(int(ordinary_limit * 0.95), ordinary_limit),
            }, finished_at=200.0, source_manifest_tree="c" * 40)
            _write_receipt(artifacts, "b-fail", "1.json", tree, "test:contract", {
                "example/failed-run": _obs(int(ordinary_limit * 0.95), ordinary_limit),
            }, status="failed", finished_at=200.0, source_manifest_tree=tree)
            (artifacts / "verification" / "b1").mkdir(parents=True)
            (artifacts / "verification" / "b1" / "1.progress.json").write_text("{}")

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
            }, finished_at=100.0, source_manifest_tree=tree)
            _write_receipt(artifacts, "b2", "1.json", tree, "test:race", {
                "example/race-hot": _obs(int(race_limit * 0.82), race_limit),
            }, finished_at=101.0, source_manifest_tree=tree)

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

            self.assertEqual(next(f for f in found if "example/stale-ceiling" in f.text).level, "warning")
            self.assertIn("ceiling_exceeded", next(f for f in found if "example/stale-ceiling" in f.text).text)
            self.assertIn("boundary_changed", next(f for f in found if "example/stale-boundary" in f.text).text)
            self.assertIn("(refactor)", next(f for f in found if "example/refactor" in f.text).text)
            self.assertIn("(race)", next(f for f in found if "example/race-hot" in f.text).text)
            unavail_f = next(f for f in found if "unavailable memory observations" in f.text)
            self.assertEqual(unavail_f.level, "info")
            self.assertIn("example/missing", unavail_f.text)

    def test_invalid_unhashable_and_duplicate_review_records_warn_without_blocking_or_retaining(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            repo = root / "repo"
            artifacts = root / "artifacts"
            reviews = root / "reviews"
            (repo / "a.go").parent.mkdir(parents=True, exist_ok=True)
            (repo / "a.go").write_text("package a\n")
            digest = hashlib.sha256((repo / "a.go").read_bytes()).hexdigest()
            reviews.mkdir()
            tree = "d" * 40
            limit = 1000 * mb.MIB
            _write_receipt(artifacts, "b1", "1.json", tree, "test:short", {
                "example/bad-disp": _obs(int(limit * 0.85), limit),
                "example/dup": _obs(int(limit * 0.85), limit),
                "example/valid-hot": _obs(int(limit * 0.82), limit),
            })
            (reviews / "01-malformed.json").write_text("{not json")
            (reviews / "02-list-lane.json").write_text(json.dumps({"package": "example/x", "lane": ["ordinary"]}))
            (reviews / "03-dict-lane.json").write_text(json.dumps({"package": "example/y", "lane": {"k": "v"}}))
            (reviews / "04-list-disposition.json").write_text(json.dumps(
                _review("example/bad-disp", "ordinary", int(limit * 0.90), {"a.go": digest}, disposition=["retain"])))
            # Two otherwise-valid duplicate records must cancel each other out so neither arbitrarily retains.
            (reviews / "05-dup-a.json").write_text(json.dumps(
                _review("example/dup", "ordinary", int(limit * 0.90), {"a.go": digest})))
            (reviews / "06-dup-b.json").write_text(json.dumps(
                _review("example/dup", "ordinary", int(limit * 0.92), {"a.go": digest})))

            found = mb.findings(repo, artifacts, tree, reviews_dir=reviews)
            self.assertTrue(all(f.level == "warning" for f in found))
            self.assertTrue(any("malformed JSON" in f.text for f in found))
            self.assertTrue(any("02-list-lane.json" in f.text for f in found))
            self.assertTrue(any("03-dict-lane.json" in f.text for f in found))
            self.assertTrue(any("04-list-disposition.json" in f.text and "disposition" in f.text for f in found))
            self.assertTrue(any("duplicate review record" in f.text and "neither record is applied" in f.text for f in found))
            # Duplicate package is NOT retained (must remain an unreviewed warning), and valid observations stay visible.
            dup_finding = next(f for f in found if f.text.startswith("example/dup (ordinary):"))
            self.assertEqual(dup_finding.level, "warning")
            self.assertNotIn("retained", dup_finding.text)
            self.assertTrue(any(f.text.startswith("example/valid-hot (ordinary):") for f in found))

            obs = {"peak_bytes": int(limit * 0.85), "limit_bytes": limit}
            bad_cases = [
                {**_review("p", "ordinary", int(limit * 0.9), {"a.go": digest}), "extra": 1},
                _review("", "ordinary", int(limit * 0.9), {"a.go": digest}),
                _review("p", "other", int(limit * 0.9), {"a.go": digest}),
                {**_review("p", "ordinary", int(limit * 0.9), {"a.go": digest}), "workstream": "BAD"},
                _review("p", "ordinary", int(limit * 0.9), {"a.go": digest}, disposition="ignore"),
                {**_review("p", "ordinary", int(limit * 0.9), {"a.go": digest}), "reason": ""},
                {**_review("p", "ordinary", int(limit * 0.9), {"a.go": digest}), "ceiling_bytes": "900"},
                _review("p", "ordinary", 10, {}, disposition="refactor"),
                _review("p", "ordinary", int(limit * 0.80), {"a.go": digest}),
                _review("p", "ordinary", int(limit * 0.90), {}),
                _review("p", "ordinary", int(limit * 0.90), {"../escape": digest}),
            ]
            for case in bad_cases:
                with self.subTest(case=case), self.assertRaises(ValueError):
                    mb.evaluate_review(case, obs, repo)
            self.assertEqual(
                mb.evaluate_review(_review("p", "ordinary", int(limit * 0.90), {"missing.go": digest}), obs, repo),
                "boundary_changed",
            )

    def test_policy_loaded_once_and_historical_batches_skipped_via_source_manifest(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            artifacts = root / "artifacts"
            tree = "e" * 40
            # Historical batch has mismatched source.json and corrupt receipt; manifest skip avoids reading it.
            old_dir = artifacts / "verification" / "b-old"
            old_dir.mkdir(parents=True)
            (old_dir / "source.json").write_text(json.dumps({"source_tree": "0" * 40}))
            (old_dir / "1.json").write_text("{corrupt")
            limit = 3584 * mb.MIB
            _write_receipt(artifacts, "b-cur", "1.json", tree, "test:digest", {
                "example/p1": _obs(int(limit * 0.81), limit),
                "example/p2": _obs(int(limit * 2.5 * 0.82), int(limit * 2.5)),
            }, source_manifest_tree=tree)
            with patch.object(mb, "load_policy", wraps=mb.load_policy) as load_policy:
                measured, _ = mb.latest_observations(artifacts, tree)
            self.assertEqual(load_policy.call_count, 1)
            self.assertIn(("ordinary", "example/p1"), measured)
            self.assertIn(("race", "example/p2"), measured)

    def test_hosted_memory_receipt_creates_post_merge_cleanup_obligation_and_suppresses_retained(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            repo = root / "repo"
            reviews = root / "reviews"
            reviews.mkdir()
            (repo / "lycaon/test/contract/retained").mkdir(parents=True)
            contract = repo / "lycaon/test/contract/retained/contract_test.go"
            contract.write_text("package retained\n")
            digest = hashlib.sha256(contract.read_bytes()).hexdigest()

            sha, tree, base = "a" * 40, "b" * 40, "c" * 40
            ordinary_limit = 3584 * mb.MIB
            (reviews / "retained.json").write_text(json.dumps(_review(
                "github.com/lycaon/lycaon/test/contract/retained", "ordinary",
                int(ordinary_limit * 0.90), {"lycaon/test/contract/retained/contract_test.go": digest},
            )))

            # Build job uploads sealed maintainability.json (no memory rows yet); sibling behavior shard uploads MU01 receipts.
            maint = {"suite": "maintainability", "tracking": {"schema_version": 1, "complete": True, "artifacts": []}}
            snapshot.seal(maint, sha, tree, base)
            build_zip = _zip_bytes({"budgets/reports/maintainability.json": maint})
            shard_zip = _zip_bytes({
                "verification/b1/1.json": {
                    "status": "passed", "source_tree": tree, "finished_at": 100.0, "names": ["test:contract"],
                    "evidence": [{"stage": "test:contract", "results": {
                        "github.com/lycaon/lycaon/test/contract/promptrender": _obs(int(ordinary_limit * 0.81), ordinary_limit),
                        "github.com/lycaon/lycaon/test/contract/retained": _obs(int(ordinary_limit * 0.85), ordinary_limit),
                    }}],
                },
                "verification/b2/1.json": {
                    "status": "passed", "source_tree": "f" * 40, "finished_at": 200.0, "names": ["test:contract"],
                    "evidence": [{"stage": "test:contract", "results": {
                        "github.com/lycaon/lycaon/test/contract/wrongtree": _obs(int(ordinary_limit * 0.95), ordinary_limit),
                    }}],
                },
            })
            shard_dup_zip = _zip_bytes({
                "verification/b3/1.json": {
                    "status": "passed", "source_tree": tree, "finished_at": 105.0, "names": ["test:contract"],
                    "evidence": [{"stage": "test:contract", "results": {
                        "github.com/lycaon/lycaon/test/contract/promptrender": _obs(int(ordinary_limit * 0.805), ordinary_limit),
                    }}],
                },
            })
            artifact_list = [
                {"id": 1, "name": "receipt-verification-nightly-build-1", "expired": False, "size_in_bytes": len(build_zip)},
                {"id": 2, "name": "receipt-verification-nightly-behavior-1", "expired": False, "size_in_bytes": len(shard_zip)},
                {"id": 3, "name": "receipt-verification-nightly-behavior-dup-1", "expired": False, "size_in_bytes": len(shard_dup_zip)},
            ]
            zips = {
                "repos/org/repo/actions/artifacts/1/zip": build_zip,
                "repos/org/repo/actions/artifacts/2/zip": shard_zip,
                "repos/org/repo/actions/artifacts/3/zip": shard_dup_zip,
            }
            run = {"id": 42, "run_attempt": 1, "head_sha": sha, "head_branch": "main",
                   "path": ".github/workflows/nightly.yml", "event": "schedule",
                   "status": "completed", "conclusion": "success", "repository": {"full_name": "org/repo"}}
            fake_holds = type("FakeHolds", (), {
                "__init__": lambda self, source: setattr(self, "records", {}) or setattr(
                    self, "changes", type("C", (), {"main": sha})()),
                "__call__": lambda self, row: False,
                "report": lambda self: [],
            })

            def fake_pages(path, key=None):
                if path.endswith("/artifacts"):
                    return artifact_list
                return []

            with patch.dict("os.environ", {"GITHUB_REPOSITORY": "org/repo"}), \
                    patch.object(evidence, "pages", side_effect=fake_pages), \
                    patch.object(evidence.subprocess, "check_output", side_effect=lambda cmd: zips[cmd[-1]]), \
                    patch.object(mb, "ROOT", repo), \
                    patch.object(evidence, "_merge_hosted_memory_artifacts",
                                 wraps=lambda r, t, a, arts: evidence._merge_hosted_memory_artifacts(
                                     r, t, a, arts, repo_root=repo, reviews_dir=reviews)), \
                    patch.object(tracker, "merged_pulls", return_value=[{"number": 2806}]), \
                    patch.object(tracker, "api", return_value={"tree": {"sha": tree}}), \
                    patch.object(tracker, "checkpoint", return_value=None), \
                    patch.object(tracker, "intake", side_effect=lambda _r, arts, _p: set(arts)), \
                    patch.object(tracker, "pages", return_value=[]), \
                    patch.object(tracker, "Holds", fake_holds):
                report = tracker.reconcile(run, dry_run=True)

            self.assertEqual(report["above_threshold"], {"package_memory_ordinary": 2})
            self.assertEqual(len(report["operations"]), 1)
            op = report["operations"][0]
            self.assertEqual(op["action"], "create")
            self.assertIn("lycaon/test/contract/promptrender", op["body"]["body"])
            self.assertNotIn("wrongtree", op["body"]["body"])
            # Retained package with valid review record is suppressed from creating an unreviewed cleanup issue.
            self.assertEqual(report["suppressed"]["retained_review"], 1)

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
                    patch.object(budgets.memory_budgets, "tracking_artifacts", return_value=[]), \
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
