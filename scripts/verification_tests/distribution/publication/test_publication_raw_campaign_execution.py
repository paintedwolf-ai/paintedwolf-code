"""Final admission composition uses selected bytes and actual raw campaign roles."""
import hashlib
import json
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest.mock import patch

from linux_release.publication.campaign import raw_campaign as owner


class RawCampaignCompositionTests(unittest.TestCase):
    def fixture(self, directory):
        root = Path(directory)
        source = {"source_commit":"a"*40,"source_tree":"c"*40}
        def selection(run, artifact):
            return {"repository":"owner/repo","repository_id":42,"run_id":run,"run_attempt":1,
                    "controller_commit":"b"*40,"artifact_id":artifact,"artifact_bytes":100,"artifact_sha256":"e"*64}
        controls, plan_selected, raw_selected = selection(101,201),selection(102,202),selection(103,203)
        def stamp(selected):
            return {"format_version":1,**{key:selected[key] for key in ("repository_id","run_id","run_attempt","controller_commit")},**source}
        def write(path,value):
            path.parent.mkdir(parents=True,exist_ok=True)
            path.write_text(json.dumps(value))
        catalog={"installations":[{"id":"deb"}],"capabilities":[]}
        drivers=owner.canonical({"format_version":1,"installations":{"deb":owner.registry(catalog,"deb")}})
        final={"format_version":1,"source_commit":source["source_commit"],"artifacts":[
            {"id":"app","installation":"deb","architecture":"amd64","source_commit":source["source_commit"],
             "identity":{"kind":"file","sha256":hashlib.sha256(b"selected package").hexdigest()}}]}
        target={"id":"guest","host":"guest.invalid","port":22,"user":"qualify","identity_file":"/private/key",
                "known_hosts":"/private/known-hosts","checkout":"/checkout","runtime_bin_dirs":[],"artifact_id":"app",
                "environment_id":"fixture","final_artifacts":"/inputs/final-artifacts.json","file_root":"/inputs/packages",
                "install_root":"/opt/painted-wolf-code","output":"/outputs/leaf","nix_program":None,"case_inputs":None,"variant_id":"fixture-variant",
                "snapshot":{"kind":"package-inventory","value":"e"*64,"channel":None}}
        plan={"format_version":2,"source_commit":source["source_commit"],"driver_tree":"d"*40,"targets":[target]}
        control_root,plan_root,raw=root/"control-payload",root/"plan-payload",root/"raw-payload"
        for name in owner.CONTROL_DIRS:
            (control_root/name).mkdir(parents=True)
        (control_root/"packages/app").write_bytes(b"selected package")
        write(control_root/"control-producer.json",stamp(controls))
        write(control_root/"final-artifacts.json",final)
        write(control_root/"repositories.json",{})
        write(plan_root/"plan-producer.json",stamp(plan_selected))
        write(plan_root/"qualification-plan.json",plan)
        (raw/"control-inputs").mkdir(parents=True)
        (raw/"control-inputs/claim").write_bytes(b"this is not selected control authority")
        executed=raw/"installed-campaign"
        (executed/"evidence").mkdir(parents=True)
        (executed/"guest").mkdir()
        execution={"format_version":1,"execution_nonce":"1"*32,"source_commit":source["source_commit"],
                   "driver_tree":"d"*40,"artifact_id":"app","environment_id":"fixture","variant_id":"fixture-variant"}
        write(executed/"guest/execution-request.json",execution)
        write(executed/"guest/leaf/execution.json",execution)
        write(executed/"guest/leaf/variant.json",{"format_version":1,"variant":{"id":"fixture-variant",
              "release":{"snapshot_kind":"package-inventory","channel":None}},"snapshot":target["snapshot"],"unverified":"fixture"})
        for name,value in [("campaign.json",{**final,"results":[]}),("admission.json",{"accepted":False}),("plan.json",plan)]:
            write(executed/name,value)
        (executed/"driver-registry.json").write_bytes(drivers)
        binding={**stamp(raw_selected),"catalog_sha256":hashlib.sha256(owner.CATALOG.read_bytes()).hexdigest(),
                 "driver_registry_sha256":hashlib.sha256(drivers).hexdigest(),"control_run_id":101,"control_artifact_id":201,
                 "qualification_plan_run_id":102,"qualification_plan_artifact_id":202}
        write(raw/"qualification.json",binding)
        payloads={201:control_root,202:plan_root,203:raw}
        def download(selected,destination,**_):
            shutil.copytree(payloads[selected["artifact_id"]],destination)
        return root,source,catalog,controls,plan_selected,raw_selected,download

    def run_materialize(self, fixture):
        root,source,catalog,controls,plan,raw,download=fixture
        work=root/"work"
        work.mkdir()
        with patch.object(owner,"campaign_limits",return_value={"max_files":100000,"max_bytes":64<<30,"max_file":16<<30}), \
                patch.object(owner,"validate_plan",side_effect=lambda selected,_:(selected["source_commit"],selected["driver_tree"],selected["targets"])), \
                patch.object(owner,"select_qualification",return_value=raw),patch.object(owner,"select_control",return_value=controls), \
                patch.object(owner,"select_plan",return_value=plan),patch.object(owner,"download",side_effect=download):
            return owner.materialize(lambda path:{"tree":{"sha":"d"*40}},"owner/repo",42,103,source,catalog,work)

    def test_uses_fresh_selected_control_bytes_and_preserves_raw_admission_as_data(self):
        with tempfile.TemporaryDirectory() as temporary:
            fixture=self.fixture(temporary)
            inputs,proof=self.run_materialize(fixture)
            self.assertEqual((inputs/"packages/app").read_bytes(),b"selected package")
            self.assertEqual(json.loads((inputs/"campaign.json").read_text())["results"],[])
            self.assertFalse((inputs/"control-producer.json").exists())
            self.assertEqual(proof["qualification"]["artifact_id"],203)
            # Composition grants no admission; the full producer still re-evaluates all cases.

    def test_unexpected_raw_receipt_is_not_an_alternate_authority(self):
        with tempfile.TemporaryDirectory() as temporary:
            fixture=self.fixture(temporary)
            (Path(temporary)/"raw-payload/accepted.json").write_text('{"accepted":true}')
            with self.assertRaisesRegex(ValueError,"producer roots"):
                self.run_materialize(fixture)

    def test_raw_source_and_actual_driver_tree_are_independently_bound(self):
        for mutation in ("source","tree"):
            with self.subTest(mutation=mutation),tempfile.TemporaryDirectory() as temporary:
                fixture=self.fixture(temporary)
                path=Path(temporary)/("raw-payload/qualification.json" if mutation=="source" else "plan-payload/qualification-plan.json")
                document=json.loads(path.read_text())
                document["source_commit" if mutation=="source" else "driver_tree"]="f"*40
                path.write_text(json.dumps(document))
                with self.assertRaises(ValueError):
                    self.run_materialize(fixture)

    def test_stale_guest_output_cannot_be_retained(self):
        with tempfile.TemporaryDirectory() as temporary:
            fixture=self.fixture(temporary)
            path=Path(temporary)/"raw-payload/installed-campaign/guest/leaf/execution.json"
            document=json.loads(path.read_text())
            document["execution_nonce"]="2"*32
            path.write_text(json.dumps(document))
            with self.assertRaisesRegex(ValueError,"stale"):
                self.run_materialize(fixture)
