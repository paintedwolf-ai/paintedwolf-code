"""Prepared release artifacts are accepted only for the exact source commit and tree they were built from."""

import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import zipfile

import yaml

import release_preparation as preparation

ROOT = Path(__file__).resolve().parents[3]
REPOSITORY = "owner/repo"
IDENTITY = 7


def commit(directory: Path, message: str) -> str:
    subprocess.run(["git", "add", "."], cwd=directory, check=True)
    subprocess.run(["git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test",
                    "commit", "-qm", message], cwd=directory, check=True)
    return subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=directory, text=True).strip()


class SourceFixture(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.checkout = self.root / "checkout"
        self.checkout.mkdir()
        subprocess.run(["git", "init", "-q"], cwd=self.checkout, check=True)
        (self.checkout / "source.txt").write_text("first\n")
        self.first = commit(self.checkout, "First")
        (self.checkout / "source.txt").write_text("second\n")
        self.second = commit(self.checkout, "Second")
        previous = Path.cwd()
        os.chdir(self.checkout)
        self.addCleanup(os.chdir, previous)
        self.plan = preparation.plan(json.loads((ROOT / "packaging/release-platforms.json").read_text()))

    def artifact(self, name: str, kind: str = "linux-packages", source: str | None = None) -> Path:
        directory = self.root / name
        (directory / "cache").mkdir(parents=True)
        (directory / "package.deb").write_bytes(b"package bytes")
        (directory / "cache" / "closure.nar").write_bytes(b"closure bytes")
        preparation.stamp(directory, name, kind, source or self.second)
        return directory


class PlanTests(SourceFixture):
    def test_catalog_yields_two_package_sets_two_closures_and_one_unsigned_app(self):
        self.assertEqual(preparation.names(self.plan), {
            "prepared-linux-x86_64": "linux-packages", "prepared-linux-aarch64": "linux-packages",
            "prepared-nix-x86_64-linux": "nix-closure", "prepared-nix-aarch64-linux": "nix-closure",
            "prepared-darwin-aarch64": "macos-app"})
        runners = {row["system"]: row["runner"] for row in self.plan["nix"]["include"]}
        self.assertEqual(runners, {"x86_64-linux": "ubuntu-24.04", "aarch64-linux": "ubuntu-24.04-arm"})

    def test_catalog_without_one_public_macos_row_is_refused(self):
        catalog = json.loads((ROOT / "packaging/release-platforms.json").read_text())
        for row in catalog["platforms"]:
            if row["goos"] == "darwin":
                row["publication"] = "candidate"
        with self.assertRaises(ValueError):
            preparation.plan(catalog)


class StampTests(SourceFixture):
    def test_stamp_records_commit_tree_and_digest_of_every_file(self):
        directory = self.artifact("prepared-linux-x86_64")
        record = json.loads((directory / preparation.STAMP).read_text())
        tree = subprocess.check_output(["git", "rev-parse", self.second + "^{tree}"], text=True).strip()
        self.assertEqual((record["source_commit"], record["source_tree"]), (self.second, tree))
        self.assertEqual([row["path"] for row in record["files"]], ["cache/closure.nar", "package.deb"])
        self.assertEqual(preparation.verify(directory, "prepared-linux-x86_64", self.second, tree)["digest"], record["digest"])

    def test_stamp_refuses_a_checkout_of_another_commit(self):
        with self.assertRaises(preparation.Refused):
            self.artifact("prepared-linux-x86_64", source=self.first)

    def test_consumer_refuses_another_source_or_changed_bytes(self):
        tree = preparation.source_tree(self.second)
        mutations = {
            "commit": lambda directory: preparation.verify(directory, "prepared-linux-x86_64", self.first,
                                                           preparation.source_tree(self.first)),
            "tree": lambda directory: preparation.verify(directory, "prepared-linux-x86_64", self.second,
                                                         preparation.source_tree(self.first)),
            "name": lambda directory: preparation.verify(directory, "prepared-linux-aarch64", self.second, tree),
            "changed": lambda directory: ((directory / "package.deb").write_bytes(b"other bytes"),
                                          preparation.verify(directory, "prepared-linux-x86_64", self.second, tree)),
            "added": lambda directory: ((directory / "extra.rpm").write_bytes(b"extra"),
                                        preparation.verify(directory, "prepared-linux-x86_64", self.second, tree)),
            "removed": lambda directory: ((directory / "cache" / "closure.nar").unlink(),
                                          preparation.verify(directory, "prepared-linux-x86_64", self.second, tree)),
            "linked": lambda directory: ((directory / "link").symlink_to(directory / "package.deb"),
                                         preparation.verify(directory, "prepared-linux-x86_64", self.second, tree)),
        }
        for label, mutate in mutations.items():
            with self.subTest(label), self.assertRaises(preparation.Refused):
                mutate(self.artifact(f"case-{label}/prepared-linux-x86_64"))

    def test_verify_cli_exits_nonzero_for_another_commit(self):
        directory = self.artifact("prepared-nix-x86_64-linux", "nix-closure")
        arguments = ["verify", "--artifact", str(directory), "--name", "prepared-nix-x86_64-linux"]
        self.assertEqual(preparation.main([*arguments, "--source-commit", self.second]), 0)
        self.assertEqual(preparation.main([*arguments, "--source-commit", self.first]), 1)


class ReceiptFixture(SourceFixture):
    def stamps(self, source=None):
        stamps = self.root / "stamps"
        for name, kind in preparation.names(self.plan).items():
            directory = self.artifact(name, kind, source)
            (stamps / name).mkdir(parents=True, exist_ok=True)
            (stamps / name / preparation.STAMP).write_text((directory / preparation.STAMP).read_text())
        return stamps

    def listed(self):
        return [{"id": index, "name": name, "expired": False}
                for index, name in enumerate(sorted(preparation.names(self.plan)), start=100)]

    def environment(self):
        return {"GITHUB_REPOSITORY": REPOSITORY, "GITHUB_RUN_ID": "50", "GITHUB_RUN_ATTEMPT": "1", "GITHUB_SHA": self.second}


class ReceiptTests(ReceiptFixture):
    def test_receipt_lists_every_artifact_with_its_retained_identity_and_digest(self):
        record = preparation.receipt(self.stamps(), self.plan, self.second, self.environment(), self.listed())
        self.assertEqual(record["source_commit"], self.second)
        self.assertEqual(len(record["artifacts"]), 5)
        self.assertTrue(all(row["artifact_id"] >= 100 and row["digest"].startswith("sha256:") for row in record["artifacts"]))

    def test_receipt_refuses_a_missing_artifact_or_a_stamp_of_another_source(self):
        stamps = self.stamps()
        with self.assertRaises(preparation.Refused):
            preparation.receipt(stamps, self.plan, self.second, self.environment(), self.listed()[1:])
        with self.assertRaises(preparation.Refused):
            preparation.receipt(stamps, self.plan, self.first, self.environment(), self.listed())


class ReuseTests(ReceiptFixture):
    def setUp(self):
        super().setUp()
        self.record = preparation.receipt(self.stamps(), self.plan, self.second, self.environment(), self.listed())
        self.run = {"id": 50, "event": "workflow_dispatch", "head_sha": self.second}
        self.row = {"id": 900, "name": preparation.RECEIPT + self.second, "expired": False, "size_in_bytes": 600,
                    "workflow_run": {"id": 50, "repository_id": IDENTITY, "head_repository_id": IDENTITY}}
        self.main = {self.second}

    def archive(self, record):
        output = io.BytesIO()
        with zipfile.ZipFile(output, "w") as archive:
            archive.writestr("receipt.json", json.dumps(record))
        return output.getvalue()

    def find(self, source=None, rows=None, run=None, record=None, listed=None):
        source = source or self.second

        def api(path):
            if path == f"repos/{REPOSITORY}":
                return {"id": IDENTITY}
            if path.startswith(f"repos/{REPOSITORY}/actions/artifacts?name="):
                name = path.partition("name=")[2].partition("&")[0]
                return {"artifacts": [row for row in ([self.row] if rows is None else rows) if row["name"] == name]}
            if path.startswith(f"repos/{REPOSITORY}/actions/runs/"):
                return run or self.run
            if path.startswith(f"repos/{REPOSITORY}/compare/"):
                base = path.rpartition("/")[2].partition("...")[0]
                return {"status": "ahead" if base in self.main else "diverged"}
            raise AssertionError(path)

        with patch.object(preparation, "api", side_effect=api), \
                patch.object(preparation, "pages", return_value=self.listed() if listed is None else listed):
            return preparation.find(REPOSITORY, source, self.plan,
                                    fetch=lambda _: self.archive(self.record if record is None else record))

    def test_second_call_for_the_same_commit_and_tree_reuses_the_receipt(self):
        self.assertEqual(self.find(), self.record)

    def test_call_for_another_commit_builds(self):
        self.assertIsNone(self.find(source=self.first))
        renamed = {**self.row, "name": preparation.RECEIPT + self.first}
        self.assertIsNone(self.find(source=self.first, rows=[renamed]))

    def test_untrusted_or_incomplete_evidence_is_never_reused(self):
        cases = {
            "fork": dict(rows=[{**self.row, "workflow_run": {**self.row["workflow_run"], "head_repository_id": 99}}]),
            "expired": dict(rows=[{**self.row, "expired": True}]),
            "oversized": dict(rows=[{**self.row, "size_in_bytes": preparation.RECEIPT_LIMIT + 1}]),
            "pull request": dict(run={**self.run, "event": "pull_request"}),
            "unreviewed controller": dict(run={**self.run, "head_sha": self.first},
                                          record={**self.record, "controller_commit": self.first}),
            "other run": dict(record={**self.record, "run_id": 51}),
            "other tree": dict(record={**self.record, "source_tree": "0" * 40}),
            "missing artifact": dict(listed=self.listed()[1:]),
            "expired artifact": dict(listed=[{**row, "expired": True} for row in self.listed()]),
            "missing kind": dict(record={**self.record, "artifacts": self.record["artifacts"][1:]}),
        }
        for label, change in cases.items():
            with self.subTest(label):
                self.assertIsNone(self.find(**change))

    def test_resolve_reports_reuse_and_builds_when_the_lookup_fails(self):
        output = self.root / "output"
        environment = {"GITHUB_REPOSITORY": REPOSITORY, "GITHUB_RUN_ID": "60", "GITHUB_OUTPUT": str(output)}
        with patch.dict(os.environ, environment), patch.object(preparation, "find", return_value=self.record):
            preparation.resolve(self.second, self.plan)
        self.assertEqual(output.read_text(), "run_id=50\nreused=true\n")
        output.unlink()
        with patch.dict(os.environ, environment), \
                patch.object(preparation, "find", side_effect=subprocess.CalledProcessError(1, ["gh"])):
            preparation.resolve(self.second, self.plan)
        self.assertEqual(output.read_text(), "run_id=60\nreused=false\n")


class WorkflowTests(unittest.TestCase):
    def workflow(self, name):
        return yaml.load((ROOT / ".github/workflows" / name).read_text(), Loader=yaml.BaseLoader)

    def test_preparation_holds_no_signing_or_publication_secret(self):
        text = (ROOT / ".github/workflows/release-prepare.yml").read_text()
        self.assertNotIn("secrets.", text)
        self.assertTrue(all("environment" not in job for job in self.workflow("release-prepare.yml")["jobs"].values()))
        callers = 0
        for path in sorted((ROOT / ".github/workflows").glob("*.yml")):
            for name, job in yaml.load(path.read_text(), Loader=yaml.BaseLoader)["jobs"].items():
                if job.get("uses") == "./.github/workflows/release-prepare.yml":
                    callers += 1
                    with self.subTest(caller=f"{path.name}:{name}"):
                        self.assertNotIn("secrets", job)
        self.assertGreaterEqual(callers, 2)

    def test_every_build_starts_together_and_the_receipt_needs_them_all(self):
        jobs = self.workflow("release-prepare.yml")["jobs"]
        builds = [name for name in jobs if name not in {"source", "receipt"}]
        self.assertEqual(sorted(builds), ["linux-packages", "macos-app", "nix-closures"])
        for name in builds:
            with self.subTest(name):
                self.assertEqual(jobs[name]["needs"], "source")
                self.assertNotIn("max-parallel", jobs[name]["strategy"])
        self.assertEqual(sorted(jobs["receipt"]["needs"]), sorted(["source", *builds]))

    def test_release_delivery_imports_prepared_closures_and_compiles_none(self):
        jobs = self.workflow("release-linux-delivery.yml")["jobs"]
        steps = [step for job in jobs.values() for step in job.get("steps", [])]
        self.assertFalse(any("nix build" in step.get("run", "") for step in steps))
        self.assertTrue(any("release_preparation verify" in step.get("run", "") for step in steps))


if __name__ == "__main__":
    unittest.main()
