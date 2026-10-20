"""Raw execution metadata cannot substitute another plan, package bundle or driver."""
import unittest

from linux_release.installed_qualification_binding import verify


class PublicationQualificationTests(unittest.TestCase):
    def fixture(self):
        selected = {"repository_id": 42, "run_id": 123, "run_attempt": 2, "controller_commit": "a"*40}
        expected = {"catalog_sha256": "c"*64, "driver_registry_sha256": "d"*64,
                    "control_run_id": 22, "control_artifact_id": 23,
                    "qualification_plan_run_id": 24, "qualification_plan_artifact_id": 25}
        document = {"format_version": 1, **selected, "source_commit": "e"*40, "source_tree": "f"*40, **expected}
        return selected, expected, document

    def test_binds_every_input_to_independent_server_and_controller_selection(self):
        selected, expected, document = self.fixture()
        verify(selected, document, "e"*40, "f"*40, **expected)
        for key, value in document.items():
            changed = "b"*len(value) if isinstance(value, str) else value+1
            with self.subTest(key=key), self.assertRaises(ValueError):
                verify(selected, {**document, key: changed}, "e"*40, "f"*40, **expected)
        for changed in [{**document,"extra":True}, {key:value for key,value in document.items() if key!="control_run_id"}]:
            with self.assertRaises(ValueError):
                verify(selected, changed, "e"*40, "f"*40, **expected)

    def test_expected_ids_and_hashes_are_typed_independent_inputs(self):
        selected, expected, document = self.fixture()
        for key,value in [("control_run_id",True),("qualification_plan_artifact_id",0),
                          ("driver_registry_sha256","bad"),("catalog_sha256",None)]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                verify(selected, document, "e"*40, "f"*40, **{**expected,key:value})
