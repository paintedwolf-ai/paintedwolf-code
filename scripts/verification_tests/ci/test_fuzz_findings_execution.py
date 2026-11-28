"""Tests for nightly fuzz finding capture, filing, gating and clearing."""

import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import fuzz_plan
from fuzz_plan import findings
from verification_plan import catalog
from verification_tests.ci.workflows import workflow

PLAN = {
    "fuzz": {
        "tags": ["integration"],
        "smoke_seconds": 1,
        "targets": {
            "./plain": {"FuzzPlain": 1},
            "./tagged": {"FuzzTagged": 2},
        },
    }
}


def _git(cwd, *args):
    result = subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True)
    if result.returncode != 0:
        raise AssertionError(f"git {' '.join(args)} in {cwd} failed:\n{result.stdout}\n{result.stderr}")
    return result.stdout.strip()


def _make_entry(package="./plain", target="FuzzPlain", finding_id="abc123",
                commit="1" * 40, run_id="42", failure_output="panic: boom\n", reproduced=True):
    return {
        "package": package,
        "target": target,
        "id": finding_id,
        "commit": commit,
        "run_id": run_id,
        "failure_output": failure_output,
        "reproduced": reproduced,
    }


def _write_artifact(artifact_dir, items):
    """Write [(rel_path, bytes, entry)] and manifest.json into artifact_dir."""
    root = Path(artifact_dir)
    root.mkdir(parents=True, exist_ok=True)
    manifest = []
    for rel_path, data, entry in items:
        dest = root / rel_path
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_bytes(data)
        manifest.append(entry)
    (root / findings.MANIFEST_NAME).write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")


class GitFixtureMixin:
    """Creates a bare origin remote with main and orphan fuzz-findings branches, plus a local clone."""

    def setUpRepo(self, with_findings_branch=True):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        self.remote = self.root / "origin.git"
        self.remote.mkdir()
        _git(self.remote, "init", "--bare", "-b", "main")

        self.repo = self.root / "work"
        _git(self.root, "clone", str(self.remote), str(self.repo))
        _git(self.repo, "config", "user.name", "Test")
        _git(self.repo, "config", "user.email", "test@example.com")
        (self.repo / "README.md").write_text("main\n", encoding="utf-8")
        _git(self.repo, "add", "README.md")
        _git(self.repo, "commit", "-m", "init main")
        _git(self.repo, "push", "-u", "origin", "main")

        if with_findings_branch:
            seed = self.root / "seed-findings"
            _git(self.root, "clone", str(self.remote), str(seed))
            _git(seed, "config", "user.name", "Test")
            _git(seed, "config", "user.email", "test@example.com")
            _git(seed, "checkout", "--orphan", findings.DEFAULT_BRANCH)
            _git(seed, "rm", "-rf", ".")
            (seed / findings.README_NAME).write_text("Open fuzz findings.\n", encoding="utf-8")
            _git(seed, "add", findings.README_NAME)
            _git(seed, "commit", "-m", "init fuzz-findings")
            _git(seed, "push", "origin", f"HEAD:refs/heads/{findings.DEFAULT_BRANCH}")
            _git(self.repo, "fetch", "origin", findings.DEFAULT_BRANCH)

    def push_concurrent_finding(self, rel_path, data, entry):
        """Simulate another CI job pushing a commit to origin/fuzz-findings."""
        other = Path(tempfile.mkdtemp(dir=self.root, prefix="peer-"))
        _git(self.root, "clone", "--branch", findings.DEFAULT_BRANCH, str(self.remote), str(other))
        _git(other, "config", "user.name", "Peer")
        _git(other, "config", "user.email", "peer@example.com")
        dest = other / rel_path
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_bytes(data)
        (other / findings.sidecar_rel_path(rel_path)).write_text(json.dumps(entry) + "\n", encoding="utf-8")
        _git(other, "add", "--", rel_path, findings.sidecar_rel_path(rel_path))
        _git(other, "commit", "-m", f"peer filed {entry['id']}")
        _git(other, "push", "origin", f"HEAD:refs/heads/{findings.DEFAULT_BRANCH}")


class ExplorationCaptureTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.module = Path(tmp.name) / "lycaon"
        self.module.mkdir()
        self.findings_dir = Path(tmp.name) / "out-findings"
        for patcher in (
            patch.object(fuzz_plan, "GO_MODULE", self.module),
            patch.object(fuzz_plan, "catalog", return_value=PLAN),
            patch.dict(os.environ, {
                "PW_FUZZ_WORKERS": "1",
                "PW_FUZZ_FINDINGS_DIR": str(self.findings_dir),
                "PW_FUZZ_SOURCE_SHA": "a" * 40,
                "GITHUB_RUN_ID": "999",
            }),
        ):
            patcher.start()
            self.addCleanup(patcher.stop)
        os.environ.pop("GITHUB_STEP_SUMMARY", None)

    def test_reproducing_and_non_reproducing_crashers_are_both_recorded(self):
        def fake_go(*args, capture=True):
            if any(arg.startswith("-fuzz=") for arg in args):
                target = [a.split("^")[1].rstrip("$") for a in args if a.startswith("-fuzz=")][0]
                pkg = args[-1]
                crash_dir = self.module / pkg / "testdata" / "fuzz" / target
                crash_dir.mkdir(parents=True, exist_ok=True)
                fid = "repro1" if target == "FuzzPlain" else "flaky2"
                (crash_dir / fid).write_bytes(f"go test fuzz v1\nstring({fid!r})\n".encode())
                return subprocess.CompletedProcess(args, 1, stdout=f"--- FAIL: {target}\npanic: {fid}\n", stderr="")
            if any("/flaky2" in arg for arg in args):
                return subprocess.CompletedProcess(args, 0, stdout="PASS\n", stderr="")
            if any("/repro1" in arg for arg in args):
                return subprocess.CompletedProcess(args, 1, stdout="FAIL\n", stderr="")
            return subprocess.CompletedProcess(args, 0, stdout="ok\n", stderr="")

        with patch.object(fuzz_plan, "listed", return_value={"./plain": ["FuzzPlain"], "./tagged": ["FuzzTagged"]}), \
             patch.object(fuzz_plan, "go", side_effect=fake_go), \
             contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            code = fuzz_plan.main(["run"])

        self.assertEqual(code, 1)
        manifest = json.loads((self.findings_dir / findings.MANIFEST_NAME).read_text(encoding="utf-8"))
        by_id = {entry["id"]: entry for entry in manifest}
        self.assertEqual(set(by_id), {"repro1", "flaky2"})
        self.assertTrue(by_id["repro1"]["reproduced"])
        self.assertFalse(by_id["flaky2"]["reproduced"])
        self.assertIn("panic: repro1", by_id["repro1"]["failure_output"])
        self.assertIn("panic: flaky2", by_id["flaky2"]["failure_output"])
        self.assertEqual(by_id["repro1"]["commit"], "a" * 40)
        self.assertEqual(by_id["flaky2"]["run_id"], "999")
        # Both staged inputs are byte-identical to what Go wrote.
        repro_rel = findings.finding_rel_path("./plain", "FuzzPlain", "repro1")
        flaky_rel = findings.finding_rel_path("./tagged", "FuzzTagged", "flaky2")
        self.assertEqual((self.findings_dir / repro_rel).read_bytes(),
                         (self.module / "plain/testdata/fuzz/FuzzPlain/repro1").read_bytes())
        self.assertEqual((self.findings_dir / flaky_rel).read_bytes(),
                         (self.module / "tagged/testdata/fuzz/FuzzTagged/flaky2").read_bytes())


class FindingsValidationAndFilingTests(GitFixtureMixin, unittest.TestCase):
    def setUp(self):
        self.setUpRepo(with_findings_branch=True)

    def test_validation_rejects_undeclared_targets_traversals_absolute_and_symlinks(self):
        targets = findings.declared_targets(PLAN)
        for bad in (
            "",
            "/lycaon/plain/testdata/fuzz/FuzzPlain/a",
            "lycaon/plain/../plain/testdata/fuzz/FuzzPlain/a",
            "lycaon/unknown/testdata/fuzz/FuzzPlain/a",
            "lycaon/plain/testdata/fuzz/FuzzUndeclared/a",
            "lycaon/plain/testdata/other/FuzzPlain/a",
            "lycaon/plain/testdata/fuzz/FuzzPlain/a.finding.json",
            "other/plain/testdata/fuzz/FuzzPlain/a",
        ):
            with self.subTest(path=bad), self.assertRaises(findings.FindingsError):
                findings.validate_finding_path(bad, targets)

        # Reject symlink inside artifact.
        art = self.root / "art-symlink"
        rel = findings.finding_rel_path("./plain", "FuzzPlain", "sym1")
        entry = _make_entry(finding_id="sym1")
        art.mkdir()
        (art / findings.MANIFEST_NAME).write_text(json.dumps([entry]), encoding="utf-8")
        dest = art / rel
        dest.parent.mkdir(parents=True)
        outside = self.root / "outside.txt"
        outside.write_text("secret", encoding="utf-8")
        dest.symlink_to(outside)
        with self.assertRaises(findings.FindingsError):
            findings.load_artifact(art, PLAN)

        # Reject unexpected extra file in artifact.
        art2 = self.root / "art-extra"
        _write_artifact(art2, [(rel, b"data", entry)])
        (art2 / "extra.txt").write_text("nope", encoding="utf-8")
        with self.assertRaises(findings.FindingsError):
            findings.load_artifact(art2, PLAN)

        # Reject malformed manifest and wrong field types.
        art3 = self.root / "art-bad-json"
        art3.mkdir()
        (art3 / findings.MANIFEST_NAME).write_text("{not-json", encoding="utf-8")
        with self.assertRaises(findings.FindingsError):
            findings.load_artifact(art3, PLAN)
        with self.assertRaises(findings.FindingsError):
            findings.validate_entry("not-a-dict", targets)
        with self.assertRaises(findings.FindingsError):
            findings.validate_entry({"package": "./plain"}, targets)
        with self.assertRaises(findings.FindingsError):
            findings.validate_entry({**_make_entry(), "reproduced": "yes"}, targets)

    def test_filing_skips_ids_already_on_branch_or_main_and_commits_new_ones(self):
        rel_branch = findings.finding_rel_path("./plain", "FuzzPlain", "onbranch")
        rel_main = findings.finding_rel_path("./plain", "FuzzPlain", "onmain")
        rel_new = findings.finding_rel_path("./tagged", "FuzzTagged", "brandnew")

        # Seed rel_branch on fuzz-findings and rel_main on main.
        self.push_concurrent_finding(rel_branch, b"b1", _make_entry(finding_id="onbranch"))
        main_file = self.repo / rel_main
        main_file.parent.mkdir(parents=True, exist_ok=True)
        main_file.write_bytes(b"m1")
        _git(self.repo, "add", "--", rel_main)
        _git(self.repo, "commit", "-m", "commit onmain")
        _git(self.repo, "push", "origin", "main")

        art = self.root / "art-file"
        _write_artifact(art, [
            (rel_branch, b"b1", _make_entry(finding_id="onbranch")),
            (rel_main, b"m1", _make_entry(finding_id="onmain")),
            (rel_new, b"n1", _make_entry(package="./tagged", target="FuzzTagged", finding_id="brandnew", reproduced=False)),
        ])
        out = io.StringIO()
        code = findings.file_findings(self.repo, art, main_ref="origin/main", plan=PLAN, stream=out)
        self.assertEqual(code, 0)

        tip = findings._fetch_branch(self.repo, "origin", findings.DEFAULT_BRANCH)
        _, stored = findings.read_branch_findings(self.repo, tip, PLAN)
        stored_paths = {p: (data, entry) for p, data, entry in stored}
        self.assertEqual(set(stored_paths), {rel_branch, rel_new})
        self.assertNotIn(rel_main, stored_paths)
        self.assertEqual(stored_paths[rel_new][0], b"n1")
        self.assertFalse(stored_paths[rel_new][1]["reproduced"])

        # Re-filing the same artifact is a clean no-op.
        out2 = io.StringIO()
        self.assertEqual(findings.file_findings(self.repo, art, main_ref="origin/main", plan=PLAN, stream=out2), 0)
        self.assertEqual(findings._fetch_branch(self.repo, "origin", findings.DEFAULT_BRANCH), tip)

    def test_filing_retries_on_non_fast_forward(self):
        rel_new = findings.finding_rel_path("./plain", "FuzzPlain", "new1")
        rel_peer = findings.finding_rel_path("./tagged", "FuzzTagged", "peer1")
        art = self.root / "art-ff"
        _write_artifact(art, [(rel_new, b"new-bytes", _make_entry(finding_id="new1"))])

        real_git = findings._git
        pushed = [0]

        def racing_git(repo, *args, **kwargs):
            if args and args[0] == "push" and pushed[0] == 0:
                pushed[0] += 1
                self.push_concurrent_finding(
                    rel_peer, b"peer-bytes",
                    _make_entry(package="./tagged", target="FuzzTagged", finding_id="peer1"),
                )
            return real_git(repo, *args, **kwargs)

        with patch.object(findings, "_git", side_effect=racing_git):
            code = findings.file_findings(self.repo, art, main_ref="origin/main", plan=PLAN, stream=io.StringIO())
        self.assertEqual(code, 0)
        self.assertEqual(pushed[0], 1)
        tip = findings._fetch_branch(self.repo, "origin", findings.DEFAULT_BRANCH)
        _, stored = findings.read_branch_findings(self.repo, tip, PLAN)
        self.assertEqual({p for p, _, _ in stored}, {rel_new, rel_peer})


class FindingsGateAndClearTests(GitFixtureMixin, unittest.TestCase):
    def setUp(self):
        self.setUpRepo(with_findings_branch=True)

    def test_gate_passes_empty_fails_unresolved_or_mismatched_and_passes_when_committed(self):
        receipt_file = self.root / "receipt.json"
        out = io.StringIO()
        code, receipt = findings.check_findings(self.repo, receipt_path=receipt_file, plan=PLAN, stream=out)
        self.assertEqual(code, 0)
        self.assertEqual(receipt["open_count"], 0)
        self.assertTrue(receipt_file.is_file())
        self.assertEqual(json.loads(receipt_file.read_text(encoding="utf-8"))["commit"], receipt["commit"])

        # Add an open finding to origin/fuzz-findings.
        rel = findings.finding_rel_path("./plain", "FuzzPlain", "open1")
        entry = _make_entry(finding_id="open1", failure_output="t.Fatal: bad input\n", reproduced=False)
        self.push_concurrent_finding(rel, b"exact-bytes", entry)

        # Unresolved when missing locally.
        out = io.StringIO()
        code, receipt = findings.check_findings(self.repo, plan=PLAN, stream=out)
        self.assertEqual(code, 1)
        self.assertEqual(receipt["unresolved"], [rel])
        self.assertIn("t.Fatal: bad input", out.getvalue())
        self.assertIn("reproduced: false", out.getvalue())
        self.assertIn("git fetch origin fuzz-findings", out.getvalue())
        self.assertIn("FuzzPlain$/open1", out.getvalue())

        # Byte mismatch still counts as unresolved.
        local = self.repo / rel
        local.parent.mkdir(parents=True, exist_ok=True)
        local.write_bytes(b"different-bytes")
        code, receipt = findings.check_findings(self.repo, plan=PLAN, stream=io.StringIO())
        self.assertEqual(code, 1)
        self.assertEqual(receipt["unresolved"], [rel])

        # Exact byte match resolves the gate, including offline mode after fetch.
        local.write_bytes(b"exact-bytes")
        _git(self.repo, "fetch", "origin", f"refs/heads/{findings.DEFAULT_BRANCH}:refs/remotes/origin/{findings.DEFAULT_BRANCH}")
        code, receipt = findings.check_findings(self.repo, offline=True, plan=PLAN, stream=io.StringIO())
        self.assertEqual(code, 0)
        self.assertEqual(receipt["unresolved_count"], 0)

    def test_gate_fails_closed_when_branch_missing_or_fetch_fails(self):
        self.setUpRepo(with_findings_branch=False)
        with self.assertRaises(findings.FindingsError):
            findings.check_findings(self.repo, plan=PLAN, stream=io.StringIO())
        with self.assertRaises(findings.FindingsError):
            findings.check_findings(self.repo, remote="no-such-remote", plan=PLAN, stream=io.StringIO())
        with self.assertRaises(findings.FindingsError):
            findings.check_findings(self.repo, offline=True, plan=PLAN, stream=io.StringIO())

    def test_branch_validation_fails_closed_on_missing_or_malformed_sidecars_and_symlinks(self):
        rel = findings.finding_rel_path("./plain", "FuzzPlain", "broken1")
        # Commit an input without a sidecar on fuzz-findings.
        peer = Path(tempfile.mkdtemp(dir=self.root, prefix="broken-"))
        _git(self.root, "clone", "--branch", findings.DEFAULT_BRANCH, str(self.remote), str(peer))
        _git(peer, "config", "user.name", "Peer")
        _git(peer, "config", "user.email", "peer@example.com")
        dest = peer / rel
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_bytes(b"raw")
        _git(peer, "add", "--", rel)
        _git(peer, "commit", "-m", "missing sidecar")
        _git(peer, "push", "origin", f"HEAD:refs/heads/{findings.DEFAULT_BRANCH}")
        with self.assertRaises(findings.FindingsError):
            findings.check_findings(self.repo, plan=PLAN, stream=io.StringIO())

        # Add a malformed sidecar.
        (peer / findings.sidecar_rel_path(rel)).write_text("{bad", encoding="utf-8")
        _git(peer, "add", "--", findings.sidecar_rel_path(rel))
        _git(peer, "commit", "-m", "malformed sidecar")
        _git(peer, "push", "origin", f"HEAD:refs/heads/{findings.DEFAULT_BRANCH}")
        with self.assertRaises(findings.FindingsError):
            findings.check_findings(self.repo, plan=PLAN, stream=io.StringIO())

        # Remove the input leaving an orphan sidecar.
        _git(peer, "rm", "--", rel)
        _git(peer, "commit", "-m", "orphan sidecar")
        _git(peer, "push", "origin", f"HEAD:refs/heads/{findings.DEFAULT_BRANCH}")
        with self.assertRaises(findings.FindingsError):
            findings.check_findings(self.repo, plan=PLAN, stream=io.StringIO())

        # Add a symlink on the branch.
        (peer / "link").symlink_to("README.md")
        _git(peer, "add", "--", "link")
        _git(peer, "commit", "-m", "symlink")
        _git(peer, "push", "origin", f"HEAD:refs/heads/{findings.DEFAULT_BRANCH}")
        with self.assertRaises(findings.FindingsError):
            findings.check_findings(self.repo, plan=PLAN, stream=io.StringIO())

    def test_clearing_removes_only_byte_identical_main_findings_and_handles_non_ff(self):
        rel_match = findings.finding_rel_path("./plain", "FuzzPlain", "match1")
        rel_diff = findings.finding_rel_path("./plain", "FuzzPlain", "diff1")
        rel_open = findings.finding_rel_path("./tagged", "FuzzTagged", "open2")
        rel_peer = findings.finding_rel_path("./tagged", "FuzzTagged", "peer2")

        self.push_concurrent_finding(rel_match, b"same", _make_entry(finding_id="match1"))
        self.push_concurrent_finding(rel_diff, b"orig", _make_entry(finding_id="diff1"))
        self.push_concurrent_finding(rel_open, b"open", _make_entry(package="./tagged", target="FuzzTagged", finding_id="open2"))

        # Commit match1 identically and diff1 with different bytes on main.
        for rel, data in ((rel_match, b"same"), (rel_diff, b"changed")):
            p = self.repo / rel
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_bytes(data)
            _git(self.repo, "add", "--", rel)
        _git(self.repo, "commit", "-m", "resolve match1")

        real_git = findings._git
        pushed = [0]

        def racing_git(repo, *args, **kwargs):
            if args and args[0] == "push" and pushed[0] == 0:
                pushed[0] += 1
                self.push_concurrent_finding(
                    rel_peer, b"peer",
                    _make_entry(package="./tagged", target="FuzzTagged", finding_id="peer2"),
                )
            return real_git(repo, *args, **kwargs)

        with patch.object(findings, "_git", side_effect=racing_git):
            code = findings.clear_findings(self.repo, main_ref="HEAD", plan=PLAN, stream=io.StringIO())
        self.assertEqual(code, 0)
        self.assertEqual(pushed[0], 1)

        tip = findings._fetch_branch(self.repo, "origin", findings.DEFAULT_BRANCH)
        _, remaining = findings.read_branch_findings(self.repo, tip, PLAN)
        self.assertEqual({p for p, _, _ in remaining}, {rel_diff, rel_open, rel_peer})

        # Second clear is a clean no-op.
        self.assertEqual(findings.clear_findings(self.repo, main_ref="HEAD", plan=PLAN, stream=io.StringIO()), 0)

    def test_cli_main_dispatches_and_returns_nonzero_on_error(self):
        with patch.object(findings, "catalog", return_value=PLAN), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(findings.main(["check", "--repo", str(self.repo)]), 0)
            self.assertEqual(findings.main(["check", "--repo", str(self.repo), "--branch", "missing"]), 1)
            art = self.root / "cli-art"
            rel = findings.finding_rel_path("./plain", "FuzzPlain", "cli1")
            _write_artifact(art, [(rel, b"x", _make_entry(finding_id="cli1"))])
            self.assertEqual(findings.main(["file", "--repo", str(self.repo), "--artifact-dir", str(art)]), 0)
            self.assertEqual(findings.main(["clear", "--repo", str(self.repo)]), 0)


class FindingsWorkflowAndPlanLaws(unittest.TestCase):
    def test_gate_is_in_ready_fast_and_merge_queue_integration_profiles(self):
        plan = catalog()
        self.assertIn("test:fuzz:findings", plan["ci"]["ready"]["targets"])
        self.assertIn("fast", plan["ci"]["ready"]["profiles"])
        self.assertIn("test:fuzz:findings", plan["ci"]["build"]["targets"])
        self.assertIn("integration", plan["ci"]["build"]["profiles"])

    def test_only_filing_and_clearing_jobs_hold_contents_write(self):
        nightly = workflow("nightly")
        self.assertEqual(nightly.permissions.get("contents"), "read")
        write_jobs = [name for name, job in nightly.jobs.items() if (job.permissions or {}).get("contents") == "write"]
        self.assertEqual(write_jobs, ["file-fuzz-findings"])

        clear = workflow("clear-fuzz-findings")
        self.assertEqual(clear.permissions.get("contents"), "read")
        clear_write = [name for name, job in clear.jobs.items() if (job.permissions or {}).get("contents") == "write"]
        self.assertEqual(clear_write, ["clear-fuzz-findings"])


if __name__ == "__main__":
    unittest.main()
