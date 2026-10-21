"""Hosted Nix builds bind only what they qualify, on disposable runners, from exact reports."""

import base64
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release import nix_qualification as qualification

HOSTED = {"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted", "GITHUB_SHA": "d" * 40}
HASH = "sha256-" + base64.b64encode(bytes(32)).decode()
APP = "/nix/store/" + "0" * 32 + "-painted-wolf-code"
VM = "/nix/store/" + "1" * 32 + "-nixos-vm"


class NixQualificationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.evidence = Path(self.temporary.name) / "evidence"
        self.evidence.mkdir()
        self.identity = dict(source_commit="a" * 40, source_tree="b" * 40, lock_sha256="c" * 64)

    def report(self, component, output, **extra):
        return {"operation": "build", "passed": True, "component": component, **self.identity,
                "builds": [{"outputs": {"out": output}}], **extra}

    def test_scanner_authorization_requires_the_admitted_output(self):
        selected = {"output": APP, "executable": APP + "/bin/opengrep-artifact"}
        authorized = []

        def authorize(executable):
            authorized.append(executable)
            return {"executable": executable, "profile_loaded": True}

        qualification.qualify(self.report("scanner-host-tools", APP, scanner_tools=selected),
                              self.evidence, HOSTED, authorize=authorize)
        self.assertEqual(authorized, [selected["executable"]])
        self.assertTrue(json.loads((self.evidence / "scanner-builder-permission.json").read_text())["profile_loaded"])
        with self.assertRaisesRegex(ValueError, "differs"):
            qualification.qualify(self.report("scanner-host-tools", VM, scanner_tools=selected),
                                  self.evidence, HOSTED, authorize=authorize)
        self.assertEqual(len(authorized), 1)

    def test_retained_closures_record_their_source_runner_and_scope(self):
        archive = {"publication": "nonrelease", "path": "/tmp/closure"}
        with patch.object(qualification.platform, "machine", return_value="aarch64"):
            qualification.qualify(self.report("default", APP, archive=archive), self.evidence, HOSTED)
            qualification.qualify(self.report("shell", APP, archive=archive), self.evidence, HOSTED)
        application = json.loads((self.evidence / "qualification-source.json").read_text())
        self.assertEqual(application, {**self.identity, "architecture": "aarch64", "controller_commit": "d" * 40,
                                       "execution": "native", "publication": "nonrelease",
                                       "scope": "Nix closure build; installed desktop qualification is separate"})
        component = json.loads((self.evidence / "component-source.json").read_text())
        self.assertEqual(component["component"], "shell")
        for report, environment in ((self.report("default", APP, archive={"publication": "release"}), HOSTED),
                                    (self.report("default", APP), HOSTED),
                                    (self.report("default", APP, archive=archive), {**HOSTED, "GITHUB_SHA": "main"})):
            with self.subTest(report=report.get("archive"), sha=environment["GITHUB_SHA"]), self.assertRaises(ValueError):
                qualification.qualify(report, self.evidence, environment)

    def test_factory_binds_the_admitted_application_and_writes_into_its_archive(self):
        closure = {APP: {"narHash": HASH, "narSize": 13, "references": [], "deriver": APP + ".drv"}}
        application = self.report("default", APP, archive={"publication": "nonrelease"}, closure=closure)
        (self.evidence / "nix").mkdir()
        (self.evidence / "nix/application-producer.json").write_text(json.dumps(application))
        archive = Path(self.temporary.name) / "factory"
        archive.mkdir()
        factory = {"schema_version": 1, "application_commit": "a" * 40, "source_commit": "e" * 40,
                   "source_tree": "f" * 40, "channel": "nixos-unstable", "nixpkgs_revision": "1" * 40,
                   "lock_sha256": "2" * 64}
        vm_closure = {**closure, VM: {"narHash": HASH, "narSize": 23, "references": [APP], "deriver": VM + ".drv"}}
        report = self.report("nixos-plasma-vm", VM, archive={"publication": "nonrelease", "path": str(archive)},
                             closure=vm_closure, factory=factory)
        with patch.object(qualification.platform, "machine", return_value="x86_64"):
            qualification.qualify(report, self.evidence, HOSTED)
        manifest = json.loads((archive / "paintedwolf-vm-qualification.json").read_text())
        self.assertEqual((manifest["desktop"], manifest["application"]["store_path"]), ("plasma", APP))
        self.assertEqual(json.loads((self.evidence / "nixos-plasma-qualification.json").read_text()), manifest)
        del report["closure"][APP]
        with patch.object(qualification.platform, "machine", return_value="x86_64"), self.assertRaises(ValueError):
            qualification.qualify(report, self.evidence, HOSTED)

    def test_only_passed_hosted_builds_of_known_components_qualify(self):
        archive = {"publication": "nonrelease"}
        for report, environment in ((self.report("default", APP, archive=archive), {}),
                                    ({**self.report("default", APP, archive=archive), "passed": False}, HOSTED),
                                    ({**self.report("default", APP, archive=archive), "operation": "check"}, HOSTED),
                                    (self.report("bun-lock", APP), HOSTED)):
            with self.subTest(report=report["component"], environment=environment), self.assertRaises(ValueError):
                qualification.qualify(report, self.evidence, environment)
        self.assertEqual(list(self.evidence.iterdir()), [])


if __name__ == "__main__":
    unittest.main()
