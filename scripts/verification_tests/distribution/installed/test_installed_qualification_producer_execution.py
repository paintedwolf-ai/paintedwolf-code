"""Qualification retention refuses incomplete observations and unprotected execution."""
import argparse
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from linux_release import installed_qualification_producer as producer


class InstalledQualificationProducerTests(unittest.TestCase):
    LOCAL = {"execution": "local", "repository_id": 7, "controller_commit": "c" * 40, "host": "qualification-host"}

    def prepare(self, output, plan_controller):
        selection = lambda role, controller: {"repository": "owner/repo", "repository_id": 7, "run_id": 3,
                                              "run_attempt": 1, "controller_commit": controller, "artifact_id": 4, "role": role}
        with patch.object(producer, "context", return_value=(self.LOCAL, "t" * 40, {}, b"drivers")), \
                patch.object(producer, "select_control", return_value=selection("controls", "c" * 40)), \
                patch.object(producer, "select_plan", return_value=selection("plan", plan_controller)), \
                patch.object(producer, "download") as download, \
                patch.object(producer, "validate_inputs") as validate:
            producer.prepare(argparse.Namespace(repository="owner/repo", control_run_id=1, control_artifact_id=2,
                                                qualification_plan_run_id=3, qualification_plan_artifact_id=4,
                                                source_commit="a" * 40, source_tree="b" * 40, output=output))
        return download, validate

    def test_local_controller_must_be_the_release_controller_that_made_the_plan(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "prepared"
            with self.assertRaisesRegex(ValueError, "release controller commit that produced its plan"):
                self.prepare(output, "d" * 40)
            self.assertFalse(output.exists())

    def test_prepare_downloads_both_selections_and_records_the_local_producer(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "prepared"
            download, validate = self.prepare(output, "c" * 40)
            self.assertEqual([call.args[1].name for call in download.call_args_list], ["controls", "plan"])
            validate.assert_called_once()
            document = json.loads((output / "preparation.json").read_text())
            self.assertEqual(document["producer"], self.LOCAL)
            self.assertEqual((document["source_commit"], document["driver_tree"]), ("a" * 40, "t" * 40))

    def test_context_binds_the_clean_local_checkout_and_repository(self):
        with patch.object(producer.subprocess, "check_output", side_effect=["c" * 40 + "\n", "t" * 40 + "\n"]), \
                patch.object(producer, "current_tree", return_value="t" * 40), \
                patch.object(producer, "api", return_value={"id": 7}), \
                patch.object(producer.socket, "gethostname", return_value="qualification-host"), \
                patch.object(producer, "read", return_value={"installations": []}), \
                patch.object(producer, "registry", return_value={}):
            selected, tree, _, _ = producer.context("owner/repo")
        self.assertEqual((selected, tree), (self.LOCAL, "t" * 40))
        with patch.object(producer.subprocess, "check_output", side_effect=["c" * 40, "t" * 40]), \
                patch.object(producer, "current_tree", return_value="u" * 40), \
                patch.object(producer, "api", return_value={"id": 7}), \
                self.assertRaisesRegex(ValueError, "immutable controller source tree"):
            producer.context("owner/repo")

    def test_lane_entry_routes_prepare_and_retain(self):
        from linux_release import installed_results
        with patch.object(producer, "prepare") as prepare, \
                patch.object(installed_results.sys, "argv", ["installed_results", "--prepare-qualification",
                    "--source-commit", "a" * 40, "--source-tree", "b" * 40, "--control-run-id", "1", "--control-artifact-id", "2",
                    "--qualification-plan-run-id", "3", "--qualification-plan-artifact-id", "4", "--output", "out"]):
            self.assertEqual(installed_results.main(), 0)
        self.assertEqual(prepare.call_args.args[0].repository, "paintedwolf-ai/paintedwolf-code")
        with patch.object(producer, "retain") as retain, \
                patch.object(installed_results.sys, "argv", ["installed_results", "--retain-qualification",
                    "--prepared", "p", "--execution", "e", "--output", "o"]):
            self.assertEqual(installed_results.main(), 0)
        retain.assert_called_once()

    def test_failed_actual_campaign_cannot_be_retained_using_an_accepted_receipt(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            execution = root/"executed"
            execution.mkdir()
            controls = root/"fresh/controls"
            controls.mkdir(parents=True)
            (controls/"final-artifacts.json").write_text('{}')
            plan = {"targets": []}
            for name,value in [("plan.json",plan),("campaign.json",{}),("admission.json",{"accepted":True})]:
                (execution/name).write_text(json.dumps(value))
            (execution/"driver-registry.json").write_bytes(b"drivers")
            args = argparse.Namespace(execution=execution,output=root/"raw")
            with patch.object(producer,"validate_inputs",return_value=plan), \
                    patch.object(producer,"admit",return_value={"accepted":False}) as admission, \
                    patch.object(producer,"retain_tree") as retain, self.assertRaisesRegex(ValueError,"freshly admitted"):
                producer.retain_verified(args,{},"a"*40,{},b"drivers",{"selections":{},"source_commit":"a"*40},root/"fresh")
            admission.assert_called_once()
            retain.assert_not_called()
            self.assertFalse(args.output.exists())

    def test_changed_executed_plan_and_driver_registry_refuse_before_admission(self):
        with tempfile.TemporaryDirectory() as temporary:
            execution = Path(temporary)/"executed"
            execution.mkdir()
            (execution/"plan.json").write_text('{"targets":[]}')
            (execution/"driver-registry.json").write_bytes(b"changed")
            args = argparse.Namespace(execution=execution,output=Path(temporary)/"raw")
            with patch.object(producer,"validate_inputs",return_value={"targets":[]}), \
                    patch.object(producer,"admit") as admission, self.assertRaisesRegex(ValueError,"fixed driver"):
                producer.retain_verified(args,{},"a"*40,{},b"drivers",{"selections":{}},Path(temporary))
            admission.assert_not_called()
            self.assertFalse(args.output.exists())

    def test_stale_guest_nonce_refuses_before_campaign_admission(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)
            execution=root/"executed"
            leaf=execution/"guest/leaf"
            leaf.mkdir(parents=True)
            target={"id":"guest","artifact_id":"app","environment_id":"fixture","variant_id":"fixture-variant","case_selection":None,
                    "snapshot":{"kind":"package-inventory","value":"e"*64,"channel":None}}
            plan={"targets":[target]}
            (execution/"plan.json").write_text(json.dumps(plan))
            (execution/"driver-registry.json").write_bytes(b"drivers")
            receipt={"format_version":1,"execution_nonce":"1"*32,"source_commit":"b"*40,
                     "driver_tree":"a"*40,"artifact_id":"app","environment_id":"fixture","variant_id":"fixture-variant", 'case_selection': None}
            (execution/"guest/execution-request.json").write_text(json.dumps(receipt))
            receipt["execution_nonce"]="2"*32
            (leaf/"execution.json").write_text(json.dumps(receipt))
            args=argparse.Namespace(execution=execution,output=root/"raw")
            with patch.object(producer,"validate_inputs",return_value=plan), \
                    patch.object(producer,"admit") as admission,self.assertRaisesRegex(ValueError,"stale"):
                producer.retain_verified(args,{},"a"*40,{},b"drivers",{"source_commit":"b"*40,"selections":{}},root/"fresh")
            admission.assert_not_called()
            self.assertFalse(args.output.exists())

    def test_plan_shape_is_owned_by_execution_validator_and_source_stays_independent(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)
            controls,plan=root/"controls",root/"plan"
            controls.mkdir()
            plan.mkdir()
            for name in producer.CONTROL_DIRS:
                (controls/name).mkdir()
            for name in producer.CONTROL_FILES:
                (controls/name).write_text("{}")
            for name in producer.PLAN_FILES:
                (plan/name).write_text("{}")
            document={"format_version":2,"source_commit":"a"*40,"driver_tree":"b"*40,"targets":[]}
            (plan/"qualification-plan.json").write_text(json.dumps(document))
            with patch.object(producer,"verify_producer"), \
                    patch.object(producer,"validate_plan",return_value=("a"*40,"b"*40,[])) as validate:
                self.assertEqual(producer.validate_inputs(root,{"controls":{},"plan":{}},
                                 {"source_commit":"a"*40,"source_tree":"c"*40},"b"*40),document)
                validate.assert_called_once_with(document,{})
                validate.return_value=("d"*40,"b"*40,[])
                with self.assertRaisesRegex(ValueError,"selected source"):
                    producer.validate_inputs(root,{"controls":{},"plan":{}},
                                             {"source_commit":"a"*40,"source_tree":"c"*40},"b"*40)
