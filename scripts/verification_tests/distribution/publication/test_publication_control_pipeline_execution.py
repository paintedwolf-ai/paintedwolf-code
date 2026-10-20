"""Protected control assembly preserves candidate provenance and final plan ownership."""
from pathlib import Path
import json
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release.publication import control_pipeline as owner

SOURCE = {"source_commit": "a" * 40, "source_tree": "b" * 40}
PRODUCER = {"repository_id": 1, "run_id": 2, "run_attempt": 1, "controller_commit": "c" * 40}
SELECTION = {"run_id": 3, "controller_commit": "d" * 40, "artifacts": {"amd64": 4, "arm64": 5}}


def final(architecture):
    return {"format_version": 1, "source_commit": SOURCE["source_commit"], "artifacts": [
        {"id": f"app-{architecture}.deb", "source_commit": SOURCE["source_commit"], "installation": "deb",
         "architecture": architecture, "identity": {"kind": "file", "sha256": "e" * 64}}]}


class ControlPipelineTests(unittest.TestCase):
    def execute(self, root, *, stale=False):
        seen = []
        def candidate(api, repository, repository_id, run_id, artifact_id, controller,
                      source_repository, commit, tree, architecture, output, *, partial):
            self.assertFalse(partial)
            self.assertEqual((commit, tree), (SOURCE["source_commit"], SOURCE["source_tree"]))
            output.mkdir(); (output / "packages").mkdir()
            manifest = final(architecture)
            (output / "final-artifacts.json").write_text(json.dumps(manifest))
            (output / "packages" / manifest["artifacts"][0]["id"]).write_bytes(b"candidate")
            seen.append(architecture)
            return {"provenance": {"artifact_id": artifact_id, "run_attempt": 1}}
        def build(manifest, packages, output, *arguments, **keywords):
            self.assertEqual(set(seen), {"amd64", "arm64"})
            self.assertEqual(len(manifest["artifacts"]), 2)
            self.assertFalse(keywords["rehearsal"])
            output.mkdir()
            (output / "final-artifacts.json").write_text(json.dumps(manifest))
            return {"published": False}
        def selected(*arguments):
            return {"artifact_id": arguments[4], "run_attempt": 2 if stale else 1}
        output = root / "result"
        source = root / "source"
        source.mkdir()
        with patch.object(owner, "facts", return_value=SOURCE), \
                patch.object(owner, "candidate", side_effect=candidate), \
                patch.object(owner, "build", side_effect=build), \
                patch.object(owner, "plan", return_value={"targets": ["actual-plan"]}), \
                patch.object(owner, "select_candidate", side_effect=selected):
            try:
                return owner.produce(None, "owner/repo", PRODUCER, SELECTION, source, SOURCE["source_commit"],
                                     SOURCE["source_tree"], "f" * 40, output,
                                     catalog={}, signing_catalog=root / "signing", private_key=root / "key",
                                     repository_base_url="https://example.invalid", previous={}, nix_inputs=[], fleet_template={}), output
            except ValueError:
                self.assertFalse(output.exists())
                raise

    def test_complete_native_roles_are_reselected_after_signing_before_plan_retention(self):
        with tempfile.TemporaryDirectory() as directory:
            report, output = self.execute(Path(directory))
            self.assertFalse(report["application_qualified"])
            self.assertFalse(report["published"])
            self.assertEqual(report["plan_targets"], 1)
            proof = json.loads((output / "plan/plan-producer.json").read_text())
            self.assertEqual(proof, {"format_version": 1, **PRODUCER, **SOURCE})

    def test_changed_native_run_attempt_removes_unadmitted_controls_and_plan(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(ValueError, "producer changed"):
                self.execute(Path(directory), stale=True)

    def test_partial_cpu_selection_refuses_before_source_or_download(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(owner, "facts") as source:
            selection = {**SELECTION, "artifacts": {"amd64": 4}}
            with self.assertRaisesRegex(ValueError, "both CPUs"):
                owner.produce(None, "owner/repo", PRODUCER, selection, Path(directory), "a" * 40,
                              "b" * 40, "f" * 40, Path(directory) / "result", catalog={},
                              signing_catalog=Path(directory), private_key=Path(directory), repository_base_url="",
                              previous={}, nix_inputs=[], fleet_template={})
            source.assert_not_called()

    def test_fleet_selector_is_resolved_from_postsign_artifact_identity_and_owner_validates_coverage(self):
        target = {key: None for key in owner.FIELDS - {"artifact_id"}}
        target["artifact_selector"] = {"installation": "deb", "architecture": "amd64"}
        template = {"format_version": 1, "targets": [target]}
        catalog = {"protected": "catalog"}
        with patch.object(owner, "validate_plan") as validate:
            result = owner.plan(template, final("amd64"), SOURCE, "f" * 40, catalog)
            self.assertEqual(result["targets"][0]["artifact_id"], "app-amd64.deb")
            self.assertEqual(result["format_version"], owner.PLAN_FORMAT_VERSION)
            self.assertIn("input_adapter", result["targets"][0])
            self.assertIsNone(result["targets"][0]["input_adapter"])
            self.assertNotIn("artifact_selector", result["targets"][0])
            validate.assert_called_once_with(result, final("amd64"), catalog)
            self.assertIn("artifact_selector", template["targets"][0])
        target["artifact_selector"]["architecture"] = "arm64"
        with self.assertRaisesRegex(ValueError, "absent final"):
            owner.plan(template, final("amd64"), SOURCE, "f" * 40, catalog)
