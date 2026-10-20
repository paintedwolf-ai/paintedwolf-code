"""Qualification retention refuses incomplete observations and unprotected execution."""
import argparse
from contextlib import redirect_stderr
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from linux_release import installed_qualification_producer as producer


class InstalledQualificationProducerTests(unittest.TestCase):
    def test_unprotected_dispatch_cannot_download_or_create_raw_artifacts(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary)/"prepared"
            errors = io.StringIO()
            with patch.dict(os.environ, {"GITHUB_EVENT_NAME":"pull_request", "GITHUB_REF":"refs/heads/feature"}), \
                    patch.object(producer, "download") as download, redirect_stderr(errors), self.assertRaises(SystemExit):
                producer.main(["prepare", "--source-commit", "a"*40, "--source-tree", "b"*40,
                               "--control-run-id","1","--control-artifact-id","2", "--qualification-plan-run-id","3",
                               "--qualification-plan-artifact-id","4","--output",str(output)])
            download.assert_not_called()
            self.assertFalse(output.exists())
            self.assertIn("protected main", errors.getvalue())

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
            target={"id":"guest","artifact_id":"app","environment_id":"fixture"}
            plan={"targets":[target]}
            (execution/"plan.json").write_text(json.dumps(plan))
            (execution/"driver-registry.json").write_bytes(b"drivers")
            receipt={"format_version":1,"execution_nonce":"1"*32,"source_commit":"b"*40,
                     "driver_tree":"a"*40,"artifact_id":"app","environment_id":"fixture","variant_id":None}
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
