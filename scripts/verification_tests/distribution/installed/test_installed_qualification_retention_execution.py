"""Qualification retention re-selects its controls independently and keeps complete receipts only."""
import argparse
import hashlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from linux_release.campaign import qualification_producer as producer

LOCAL = {"execution": "local", "repository_id": 7, "controller_commit": "c" * 40, "host": "qualification-host"}
SELECTIONS = {"controls": {"repository": "owner/repo", "run_id": 3, "artifact_id": 4, "role": "controls"},
              "plan": {"repository": "owner/repo", "run_id": 5, "artifact_id": 6, "role": "plan"}}
RECEIPTS = ("managed-execution.log", "transfer.log", "leaf.zip", "leaf/campaign.json", "leaf/binding.json",
            "leaf/environment.json", "leaf/variant.json", "leaf/driver-registry.json", "leaf/execution.json",
            "leaf/environment-facts.json", "execution-request.json")


def preparation(tree: str = "t" * 40, drivers: bytes = b"drivers") -> dict:
    return {"format_version": 1, "producer": dict(LOCAL), "driver_tree": tree, "source_commit": "a" * 40,
            "source_tree": "b" * 40, "selections": json.loads(json.dumps(SELECTIONS)),
            "catalog_sha256": hashlib.sha256(producer.CATALOG.read_bytes()).hexdigest(),
            "driver_registry_sha256": hashlib.sha256(drivers).hexdigest()}


def executed(root: Path, targets: list, receipts=RECEIPTS) -> argparse.Namespace:
    execution = root / "executed"
    execution.mkdir()
    (execution / "plan.json").write_text(json.dumps({"targets": targets}))
    (execution / "driver-registry.json").write_bytes(b"drivers")
    (execution / "campaign.json").write_text("{}")
    (execution / "admission.json").write_text(json.dumps({"accepted": True}))
    for target in targets:
        for name in receipts:
            path = execution / target["id"] / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("receipt")
    controls = root / "fresh" / "controls"
    controls.mkdir(parents=True)
    (controls / "final-artifacts.json").write_text('{"artifacts": []}')
    return argparse.Namespace(execution=execution, output=root / "raw")


class QualificationSelectionTests(unittest.TestCase):
    def test_api_reads_bounded_json_from_the_github_cli(self):
        with patch.object(producer.subprocess, "run", return_value=argparse.Namespace(returncode=0, stdout=b'{"id": 7}')) as run:
            self.assertEqual(producer.api("repos/owner/repo"), {"id": 7})
        self.assertEqual(run.call_args.args[0], ["gh", "api", "repos/owner/repo"])
        for result in [argparse.Namespace(returncode=1, stdout=b"{}"),
                       argparse.Namespace(returncode=0, stdout=b"[" + b"0," * (8 << 20) + b"0]")]:
            with patch.object(producer.subprocess, "run", return_value=result), self.assertRaises(ValueError):
                producer.api("repos/owner/repo")

    def test_selected_inputs_need_exactly_the_declared_root_entries(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "controls").mkdir()
            (root / "plan").mkdir()
            for name in producer.CONTROL_FILES:
                (root / "controls" / name).write_text("{}")
            for name in producer.PLAN_FILES:
                (root / "plan" / name).write_text("{}")
            with patch.object(producer, "verify_producer") as verify, self.assertRaisesRegex(ValueError, "root entries"):
                producer.validate_inputs(root, SELECTIONS, {"source_commit": "a" * 40, "source_tree": "b" * 40}, "t" * 40)
            verify.assert_not_called()

    def test_failed_preparation_leaves_no_output_behind(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "prepared"
            args = argparse.Namespace(repository="owner/repo", control_run_id=3, control_artifact_id=4,
                                      qualification_plan_run_id=5, qualification_plan_artifact_id=6,
                                      source_commit="a" * 40, source_tree="b" * 40, output=output)
            with patch.object(producer, "context", return_value=(LOCAL, "t" * 40, {}, b"drivers")), \
                    patch.object(producer, "select_control", return_value=SELECTIONS["controls"]), \
                    patch.object(producer, "select_plan", return_value=dict(SELECTIONS["plan"], controller_commit="c" * 40)), \
                    patch.object(producer, "download", side_effect=OSError("artifact expired")):
                with self.assertRaises(OSError):
                    producer.prepare(args)
            self.assertFalse(output.exists())

    def test_lane_entry_reports_refusals_as_a_failing_exit(self):
        errors = io.StringIO()
        with patch.object(producer, "prepare", side_effect=ValueError("controller tree changed")), \
                patch.object(producer.sys, "stderr", errors), self.assertRaises(SystemExit) as exit_:
            producer.main(["prepare", "--source-commit", "a" * 40, "--source-tree", "b" * 40, "--control-run-id", "3",
                           "--control-artifact-id", "4", "--qualification-plan-run-id", "5",
                           "--qualification-plan-artifact-id", "6", "--output", "out"])
        self.assertEqual(exit_.exception.code, 1)
        self.assertEqual(errors.getvalue(), "controller tree changed\n")


class QualificationRetainTests(unittest.TestCase):
    def retain(self, prepared: dict, selections=None, drivers: bytes = b"drivers"):
        selections = SELECTIONS if selections is None else selections
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            args = argparse.Namespace(repository="owner/repo", prepared=root / "prepared", execution=root / "executed",
                                      output=root / "raw")
            with patch.object(producer, "context", return_value=(LOCAL, "t" * 40, {"catalog": True}, drivers)), \
                    patch.object(producer, "read", return_value=prepared), \
                    patch.object(producer, "select_control", return_value=selections["controls"]) as control, \
                    patch.object(producer, "select_plan", return_value=selections["plan"]) as plan, \
                    patch.object(producer, "download") as download, \
                    patch.object(producer, "retain_verified") as verified:
                producer.retain(args)
            return control, plan, download, verified

    def test_retain_reselects_the_prepared_artifacts_from_the_server_and_downloads_fresh_copies(self):
        control, plan, download, verified = self.retain(preparation())
        self.assertEqual(control.call_args.args[1:], ("owner/repo", 7, 3, 4))
        self.assertEqual(plan.call_args.args[1:], ("owner/repo", 7, 5, 6))
        fresh = verified.call_args.args[-1]
        self.assertEqual([call.args[1] for call in download.call_args_list], [fresh / "controls", fresh / "plan"])
        self.assertTrue(fresh.name.startswith("paintedwolf-qualification-retain-"))
        self.assertFalse(fresh.exists())
        self.assertEqual(verified.call_args.args[1:6], (LOCAL, "t" * 40, {"catalog": True}, b"drivers", preparation()))

    def test_changed_preparation_or_server_selection_refuses_before_download(self):
        changed = [preparation(tree="u" * 40), preparation(drivers=b"other"), dict(preparation(), producer=dict(LOCAL, host="x")),
                   {key: value for key, value in preparation().items() if key != "catalog_sha256"},
                   dict(preparation(), format_version=True)]
        for index, prepared in enumerate(changed):
            with self.subTest(index=index), self.assertRaisesRegex(ValueError, "protected controller attempt"):
                self.retain(prepared)
        other = json.loads(json.dumps(SELECTIONS))
        other["plan"]["artifact_id"] = 99
        with self.assertRaisesRegex(ValueError, "independent server selection"):
            self.retain(preparation(), selections=other)


class QualificationRetainVerifiedTests(unittest.TestCase):
    TARGET = {"id": "guest", "artifact_id": "app", "input_adapter": None}

    def verified(self, args, root, targets, catalog=None):
        plan = {"targets": targets}
        report = {"accepted": True}
        (args.execution / "admission.json").write_text(json.dumps(report))
        return patch.object(producer, "validate_inputs", return_value=plan), \
            patch.object(producer, "verify_execution_receipt"), \
            patch.object(producer, "admit", return_value=report), \
            patch.object(producer, "campaign_limits", return_value={"max_files": 10, "max_bytes": 20, "max_file": 30, "other": 1}), \
            patch.object(producer, "retain_tree")

    def test_complete_campaign_is_retained_with_its_controls_and_bound_limits(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            args = executed(root, [self.TARGET])
            patches = self.verified(args, root, [self.TARGET])
            with patches[0], patches[1] as receipts, patches[2], patches[3] as limits, patches[4] as retained:
                producer.retain_verified(args, LOCAL, "t" * 40, {"catalog": True}, b"drivers", preparation(), root / "fresh")
            receipts.assert_called_once_with(args.execution / "guest", self.TARGET, "a" * 40, "t" * 40)
            limits.assert_called_once_with({"catalog": True}, {"artifacts": []})
            self.assertEqual([call.args[1] for call in retained.call_args_list],
                             [args.output / "control-inputs", args.output / "installed-campaign"])
            self.assertEqual(retained.call_args_list[1].kwargs["root_dirs"], {"evidence", "guest"})
            self.assertEqual(retained.call_args_list[1].kwargs["limits"], producer.Limits(max_files=10, max_bytes=20, max_file=30))
            document = json.loads((args.output / "qualification.json").read_text())
            self.assertEqual(document, {"format_version": 1, **LOCAL, "source_commit": "a" * 40, "source_tree": "b" * 40,
                                        "catalog_sha256": preparation()["catalog_sha256"],
                                        "driver_registry_sha256": preparation()["driver_registry_sha256"],
                                        "control_run_id": 3, "control_artifact_id": 4,
                                        "qualification_plan_run_id": 5, "qualification_plan_artifact_id": 6})

    def test_missing_guest_receipts_refuse_before_retention(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            args = executed(root, [self.TARGET], receipts=RECEIPTS[:-1])
            patches = self.verified(args, root, [self.TARGET])
            with patches[0], patches[1], patches[2], patches[3], patches[4] as retained, \
                    self.assertRaisesRegex(ValueError, "execution receipt"):
                producer.retain_verified(args, LOCAL, "t" * 40, {}, b"drivers", preparation(), root / "fresh")
            retained.assert_not_called()
            self.assertFalse(args.output.exists())
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            target = dict(self.TARGET, input_adapter={"kind": "pointer"})
            args = executed(root, [target])
            patches = self.verified(args, root, [target])
            with patches[0], patches[1], patches[2], patches[3], patches[4], self.assertRaisesRegex(ValueError, "execution receipt"):
                producer.retain_verified(args, LOCAL, "t" * 40, {}, b"drivers", preparation(), root / "fresh")
            (args.execution / "guest" / "native-input.jsonl").write_text("")
            (args.execution / "guest" / "native-input-transport.log").write_text("")
            patches = self.verified(args, root, [target])
            with patches[0], patches[1], patches[2], patches[3], patches[4]:
                producer.retain_verified(args, LOCAL, "t" * 40, {}, b"drivers", preparation(), root / "fresh")
            self.assertTrue((args.output / "qualification.json").is_file())

    def test_interrupted_retention_removes_the_partial_output(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            args = executed(root, [self.TARGET])
            patches = self.verified(args, root, [self.TARGET])
            with patches[0], patches[1], patches[2], patches[3], patches[4] as retained:
                retained.side_effect = OSError("disk full")
                with self.assertRaises(OSError):
                    producer.retain_verified(args, LOCAL, "t" * 40, {}, b"drivers", preparation(), root / "fresh")
            self.assertFalse(args.output.exists())


if __name__ == '__main__':
    unittest.main()
