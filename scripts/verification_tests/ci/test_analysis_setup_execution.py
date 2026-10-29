"""Analysis preparation excludes runtime engines and native toolchains."""
import copy
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from ci_policy import hosted_plan
from verification_plan import catalog

ROOT = Path(__file__).resolve().parents[3]


class AnalysisSetupTests(unittest.TestCase):
    def test_analysis_lanes_refuse_runtime_targets(self):
        data = copy.deepcopy(catalog())
        data["ci"]["behavior"]["setup"] = "analysis"
        with patch.object(hosted_plan, "catalog", return_value=data), self.assertRaisesRegex(ValueError, "outside Go analysis"):
            hosted_plan.lanes()

    def test_analysis_prepares_source_tools_and_rejects_unknown_profiles(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            scripts = root / "scripts/ci_policy"
            scripts.mkdir(parents=True)
            shutil.copyfile(ROOT / "scripts/ci_policy/provision.sh", scripts / "provision.sh")
            shutil.copyfile(ROOT / "scripts/ci_policy/go_retry.py", scripts / "go_retry.py")
            for name in ("lycaon", "scripts/commentlint", "tools"):
                (root / name).mkdir(parents=True)
            go = root / "tools/go"
            go.write_text('#!/bin/sh\nprintf "%s\\n" "$*" >> "$CALLS"\n')
            go.chmod(0o755)
            (root / "scripts/workflow_lint").mkdir()
            (root / "scripts/workflow_lint/__init__.py").write_text("")
            (root / "scripts/workflow_lint/__main__.py").write_text(
                'import os,sys\nwith open(os.environ["CALLS"], "a") as calls: calls.write("workflow " + " ".join(sys.argv[1:]) + "\\n")\n')
            calls = root / "calls"
            environment = {**os.environ, "PATH": str(root / "tools") + os.pathsep + os.environ["PATH"], "CALLS": str(calls)}
            for args, code in [(["analysis"], 0), (["unknown"], 2), (["analysis", "extra"], 2)]:
                result = subprocess.run(["bash", str(scripts / "provision.sh"), *args], env=environment,
                                        capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, code, result.stderr)
            self.assertEqual(calls.read_text().splitlines(), ["mod download", "mod download", "workflow --provision"])

    def test_catalog_selects_analysis_without_changing_checks_or_capacity(self):
        rows = {row["lane"]: row for row in hosted_plan.matrix("check")["include"]}
        for lane, tools in [("lint", "lint"), ("vulnerabilities", "vulnerabilities")]:
            self.assertEqual(rows[lane]["setup"], "analysis")
            self.assertEqual(rows[lane]["analysis"], tools)
        self.assertEqual(hosted_plan.max_parallel("check"), 3)
