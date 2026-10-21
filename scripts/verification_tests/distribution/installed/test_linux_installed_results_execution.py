"""Missing, failed, stale and nonnative installed evidence cannot certify Linux."""

import copy
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from linux_release.installed_results import admit
from linux_release.support_matrix import CATALOG, expand

SOURCE = "a" * 40


class LinuxInstalledResultsTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.proof = self.root / "native.txt"
        self.proof.write_text("native collector observation\n", encoding="utf-8")
        self.catalog = json.loads(CATALOG.read_text())
        environment = self.catalog["environments"][0]
        environment["architectures"] = [self.catalog["architectures"][0]["id"]]
        environment["installations"] = [self.catalog["installations"][0]["id"]]
        self.catalog["environments"] = [environment]
        self.catalog["architectures"] = self.catalog["architectures"][:1]
        self.catalog["installations"] = self.catalog["installations"][:1]
        self.catalog["capabilities"] = [next(value for value in self.catalog["capabilities"]
                                                   if value["tier"] == tier)
                                        for tier in ("installed-functional", "native-desktop",
                                                     "native-security", "native-performance")]
        for name in ("mixed_capabilities", "uniform_capabilities"):
            environment["qualification"]["scaling"][name] = [key for key in environment["qualification"]["scaling"][name]
                                                               if key in {item["id"] for item in self.catalog["capabilities"]}]
        artifact = {"id": "candidate", "installation": environment["installations"][0],
                    "architecture": environment["architectures"][0], "source_commit": SOURCE,
                    "identity": {"kind": "file", "sha256": "b" * 64}}
        self.final = {"format_version": 1, "source_commit": SOURCE, "artifacts": [artifact]}
        self.campaign = {**copy.deepcopy(self.final), "results": []}
        proof = {"path": "native.txt", "sha256": hashlib.sha256(self.proof.read_bytes()).hexdigest()}
        for row in expand(self.catalog):
            self.campaign["results"].append({
                "row_id": row["id"], "source_commit": SOURCE, "artifact_id": "candidate",
                "tier": row["tier"], "execution": "native", "status": "passed",
                "environment_facts": {key: "recorded fixture fact"
                                      for key in self.catalog["recorded_environment_facts"]},
                "cases": [{"id": case, "status": "passed", "evidence": [proof]}
                          for case in row["cases"]],
            })

    def report(self):
        return admit(self.catalog, self.campaign, self.final, SOURCE, self.root)

    def test_complete_campaign_retains_native_checklist_and_all_case_identities(self):
        result = self.report()
        self.assertTrue(result["accepted"])
        self.assertEqual(len(result["rows"]), 4)
        self.assertEqual(len(result["manual_checklist"]), 3)
        self.assertTrue(all(row["status"] == "passed" for row in result["rows"]))

    def test_required_missing_failed_skipped_and_unverified_results_never_pass(self):
        original = copy.deepcopy(self.campaign)
        for status in ("failed", "skipped", "unverified"):
            self.campaign = copy.deepcopy(original)
            self.campaign["results"][0]["cases"][0]["status"] = status
            result = self.report()
            self.assertFalse(result["accepted"])
            expected = "failed" if status == "failed" else "unverified"
            self.assertEqual(result["rows"][0]["status"], expected)
        self.campaign = copy.deepcopy(original)
        self.campaign["results"].pop()
        self.assertFalse(self.report()["accepted"])
        self.campaign = copy.deepcopy(original)
        self.campaign["results"][0]["cases"].pop()
        self.assertFalse(self.report()["accepted"])

    def test_emulation_only_satisfies_declared_functional_cases(self):
        functional = next(row for row in self.campaign["results"] if row["tier"] == "installed-functional")
        functional["execution"] = "emulated"
        self.assertTrue(self.report()["accepted"])
        for row in self.campaign["results"]:
            if row["tier"] == "installed-functional":
                continue
            row["execution"] = "emulated"
            self.assertFalse(self.report()["accepted"])
            row["execution"] = "native"

    def test_exact_source_final_bytes_and_artifact_coverage_are_required(self):
        original = copy.deepcopy(self.campaign)
        self.campaign["results"][0]["source_commit"] = "c" * 40
        self.assertFalse(self.report()["accepted"])
        self.campaign = copy.deepcopy(original)
        self.campaign["artifacts"][0]["identity"]["sha256"] = "d" * 64
        with self.assertRaises(ValueError):
            self.report()
        self.campaign = copy.deepcopy(original)
        self.campaign["source_commit"] = "c" * 40
        with self.assertRaises(ValueError):
            self.report()
        self.campaign = copy.deepcopy(original)
        extra = {**copy.deepcopy(self.final["artifacts"][0]), "id": "other-final-package"}
        self.final["artifacts"].append(extra)
        self.campaign["artifacts"].append(extra)
        self.assertFalse(self.report()["accepted"])

    def test_unknown_duplicate_missing_or_mismatched_fields_refuse(self):
        original = copy.deepcopy(self.campaign)
        mutations = [
            lambda c: c["results"].append(copy.deepcopy(c["results"][0])),
            lambda c: c["results"][0].update(row_id="unknown"),
            lambda c: c["results"][0].update(artifact_id="unknown"),
            lambda c: c["results"][0].update(environment_facts={}),
            lambda c: c["results"][0].update(status="available"),
            lambda c: c["results"][0].update(extra="ignored"),
            lambda c: c["results"][0]["cases"][0].update(evidence=[]),
            lambda c: c["results"][0]["cases"].append(copy.deepcopy(c["results"][0]["cases"][0])),
            lambda c: c.update(format_version=True),
        ]
        for mutation in mutations:
            self.campaign = copy.deepcopy(original)
            mutation(self.campaign)
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                self.report()
        self.campaign = copy.deepcopy(original)
        self.campaign["results"][0]["tier"] = "native-security"
        self.assertFalse(self.report()["accepted"])

    def test_retained_bytes_and_directory_boundary_are_verified(self):
        self.proof.write_text("changed\n")
        with self.assertRaises(ValueError):
            self.report()
        record = self.campaign["results"][0]["cases"][0]["evidence"][0]
        for path in ("../outside", "/absolute/path"):
            record["path"] = path
            with self.assertRaises(ValueError):
                self.report()
        record["path"] = "escape"
        (self.root / "escape").symlink_to(Path(__file__).resolve())
        with self.assertRaises(ValueError):
            self.report()

    def test_nix_closure_binds_store_nar_and_lock_identity(self):
        identity = {"kind": "nix-closure", "sha256": "e" * 64,
                    "store_path": "/nix/store/" + "f" * 32 + "-painted-wolf-code",
                    "lock_sha256": "1" * 64}
        self.final["artifacts"][0]["identity"] = identity
        self.campaign["artifacts"][0]["identity"] = copy.deepcopy(identity)
        self.assertTrue(self.report()["accepted"])
        self.campaign["artifacts"][0]["identity"].pop("lock_sha256")
        with self.assertRaises(ValueError):
            self.report()

    def test_command_emits_machine_readable_failure_for_injected_native_failure(self):
        self.campaign["results"][1]["cases"][0]["status"] = "failed"
        for name, value in (("catalog", self.catalog), ("campaign", self.campaign), ("artifacts", self.final)):
            (self.root / (name + ".json")).write_text(json.dumps(value))
        command = [sys.executable, "-m", "scripts.linux_release.installed_results",
                   "--catalog", str(self.root / "catalog.json"),
                   "--campaign", str(self.root / "campaign.json"),
                   "--final-artifacts", str(self.root / "artifacts.json"),
                   "--source-commit", SOURCE, "--evidence-root", str(self.root)]
        result = subprocess.run(command, cwd=Path(__file__).resolve().parents[4],
                                text=True, capture_output=True, timeout=10)
        self.assertNotEqual(result.returncode, 0)
        report = json.loads(result.stdout)
        self.assertFalse(report["accepted"])
        self.assertTrue(any(row["status"] == "failed" for row in report["rows"]))

    def test_cli_retains_failed_checklist_and_never_replaces_inputs_or_reports(self):
        self.campaign["results"].pop()
        for name, value in (("catalog", self.catalog), ("campaign", self.campaign), ("artifacts", self.final)):
            (self.root / (name + ".json")).write_text(json.dumps(value))
        command = [sys.executable, "-m", "scripts.linux_release.installed_results",
                   "--catalog", str(self.root / "catalog.json"),
                   "--campaign", str(self.root / "campaign.json"),
                   "--final-artifacts", str(self.root / "artifacts.json"),
                   "--source-commit", SOURCE, "--evidence-root", str(self.root), "--report"]
        cwd = Path(__file__).resolve().parents[4]
        with tempfile.TemporaryDirectory() as output_directory:
            output = Path(output_directory) / "report.json"
            result = subprocess.run(command + [str(output)], cwd=cwd, text=True, capture_output=True, timeout=10)
            self.assertEqual(result.returncode, 1, result.stderr)
            retained = output.read_bytes()
            self.assertEqual(json.loads(retained), json.loads(result.stdout))
            self.assertFalse(json.loads(retained)["accepted"])
            self.assertTrue(json.loads(retained)["manual_checklist"])
            repeated = subprocess.run(command + [str(output)], cwd=cwd, text=True, capture_output=True, timeout=10)
            self.assertNotEqual(repeated.returncode, 0)
            self.assertEqual(output.read_bytes(), retained)
        before = self.proof.read_bytes()
        refused = subprocess.run(command + [str(self.proof)], cwd=cwd, text=True, capture_output=True, timeout=10)
        self.assertNotEqual(refused.returncode, 0)
        self.assertEqual(self.proof.read_bytes(), before)
