"""The plan's fuzz section, the targets go test lists, and the runs they get."""

import contextlib
import copy
import io
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import change_report
import fuzz_plan
import verification_plan as planning

TARGET = """package {name}

import "testing"

func Fuzz{title}(f *testing.F) {{
	f.Add("seed")
	f.Fuzz(func(t *testing.T, s string) {{
		if s == "{crash}" {{
			t.Fatal("crasher")
		}}
	}})
}}
"""


class FuzzPlanTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.module = Path(temp.name) / "lycaon"
        self.package("plain", "Plain")
        self.package("tagged", "Tagged", build="//go:build integration\n\n")
        (self.module / "helper").mkdir()
        (self.module / "helper" / "helper.go").write_text("package helper\n")
        (self.module / "go.mod").write_text("module example.com/m\n\ngo 1.22\n")
        self.plan = {"fuzz": {"tags": ["integration"], "smoke_seconds": 1,
                              "targets": {"./plain": {"FuzzPlain": 1}, "./tagged": {"FuzzTagged": 2}}}}
        for patcher in (patch.object(fuzz_plan, "GO_MODULE", self.module),
                        patch.object(fuzz_plan, "catalog", side_effect=lambda: self.plan),
                        patch.dict(os.environ, {"PW_FUZZ_WORKERS": "1", "GOFLAGS": "", "GOTOOLCHAIN": "local"})):
            patcher.start()
            self.addCleanup(patcher.stop)
        os.environ.pop("GITHUB_STEP_SUMMARY", None)

    def package(self, name, title, build="", crash="never"):
        directory = self.module / name
        directory.mkdir(parents=True, exist_ok=True)
        (directory / f"{name}_test.go").write_text(build + TARGET.format(name=name, title=title, crash=crash))

    def run_main(self, *argv):
        stderr = io.StringIO()
        with contextlib.redirect_stderr(stderr), contextlib.redirect_stdout(io.StringIO()):
            code = fuzz_plan.main(list(argv))
        return code, stderr.getvalue()

    def test_listing_reads_tagged_files_the_plan_declares(self):
        self.assertEqual(fuzz_plan.listed(["integration"], ["./..."]),
                         {"./plain": ["FuzzPlain"], "./tagged": ["FuzzTagged"]})
        self.assertEqual(fuzz_plan.listed([], ["./..."]), {"./plain": ["FuzzPlain"]})

    def test_run_gives_each_target_its_declared_seconds_and_reports_them(self):
        summary = Path(self.module.parent, "summary.md")
        with patch.dict(os.environ, {"GITHUB_STEP_SUMMARY": str(summary)}):
            code, err = self.run_main("run")
        self.assertEqual(code, 0, err)
        self.assertIn("3s declared over 2 target(s)", err)
        self.assertIn("| `./tagged` | `FuzzTagged` | 2 | passed |", summary.read_text())

    def test_a_crasher_fails_the_run(self):
        self.package("plain", "Plain", crash="seed")
        code, err = self.run_main("run", "./plain")
        self.assertEqual(code, 1, err)
        self.assertIn("| `./plain` | `FuzzPlain` | 1 | failed |", err)

    def test_source_and_plan_must_name_the_same_targets(self):
        self.package("extra", "Extra")
        code, err = self.run_main("run", "./extra")
        self.assertEqual(code, 1)
        self.assertIn("./extra FuzzExtra is not declared", err)
        self.plan["fuzz"]["targets"]["./plain"]["FuzzGone"] = 1
        self.assertEqual(fuzz_plan.mismatches({"./plain": ["FuzzPlain"]}, self.plan["fuzz"]["targets"], complete=False), [])
        self.assertEqual(fuzz_plan.mismatches({"./plain": ["FuzzPlain"]}, {"./plain": {"FuzzPlain": 1, "FuzzGone": 1}}, complete=True),
                         ["./plain FuzzGone is declared but no test file defines it"])

    def test_smoke_explores_the_packages_the_change_touches(self):
        scope = change_report.Scope("base", "test", ["lycaon/tagged/testdata/fuzz/FuzzTagged/seed", "lycaon/helper/helper.go", "docs/x.md"])
        with patch.object(change_report, "resolve", return_value=scope):
            code, err = self.run_main("smoke")
        self.assertEqual(code, 0, err)
        self.assertIn("./tagged FuzzTagged: exploring 1s", err)
        self.assertNotIn("FuzzPlain", err)
        with patch.object(change_report, "resolve", return_value=None):
            self.assertEqual(self.run_main("smoke"), (0, "fuzz smoke: no change base\nfuzz: no declared target in scope\n"))

    def test_touched_packages_name_the_directory_or_its_testdata_owner(self):
        self.assertEqual(fuzz_plan.touched_packages(["lycaon/plain/plain_test.go", "lycaon/tagged/testdata/fuzz/F/x",
                                                     "lycaon/go.mod", "lycaon/missing/x.go", "scripts/x.py"]),
                         ["./plain", "./tagged"])


class FuzzDeclarationTests(unittest.TestCase):
    def test_the_catalog_refuses_a_malformed_fuzz_section(self):
        valid = planning.catalog()["fuzz"]
        self.assertTrue(planning.valid_fuzz(valid))
        for mutate in (lambda f: f.pop("tags"), lambda f: f.update(smoke_seconds=0),
                       lambda f: f["targets"].update({"internal/x": {"FuzzX": 1}}),
                       lambda f: f["targets"].update({"./x": {"Fuzzy": 1}}),
                       lambda f: f["targets"].update({"./x": {"FuzzX": 1.5}}),
                       lambda f: f["targets"].update({"./x": {}})):
            fuzz = copy.deepcopy(valid)
            mutate(fuzz)
            self.assertFalse(planning.valid_fuzz(fuzz), fuzz)


if __name__ == "__main__":
    unittest.main()
