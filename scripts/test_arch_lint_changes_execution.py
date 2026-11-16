import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import arch_lint_changes  # noqa: E402
import change_report  # noqa: E402

# The fake reads findings for its subcommand from <tree>/findings.json.
FAKE_ARCH_LINT = """#!/usr/bin/env python3
import json, sys
args = sys.argv[1:]
tree = None
for flag in ("-dir", "-root"):
    if flag in args:
        tree = args[args.index(flag) + 1]
if tree.endswith("/lycaon"):
    tree = tree[: -len("/lycaon")]
data = json.load(open(tree + "/findings.json"))
json.dump(args, open(tree + "/args-" + args[0] + ".json", "w"))
print(json.dumps(data.get(args[0], [])))
"""

FAKE_LINTER = """#!/usr/bin/env python3
import json, sys
print(json.dumps({"Issues": [{"Text": "save - mode is unused", "Pos": {"Filename": "internal/store/save.go", "Line": 7}}]}))
print("1 issues.")
sys.exit(1)
"""

POLICY = {"reach": {"roots": ["./cmd/..."], "goos": {"darwin": ["linux"], "linux": ["linux"]}, "report": "^x/", "schema": "schema.sql"},
          "vet": {"packages": ["./..."]}, "test_support": ["test$"],
          "den": {"dir": "lycaon-den", "command": [sys.executable, "../fake_knip.py"]}}

# The fake reports what <tree>/knip.json holds, from inside the Den directory.
FAKE_KNIP = """import json, os
print(open(os.path.join(os.path.dirname(os.getcwd()), "knip.json")).read() if os.path.exists("../knip.json") else "{}")
"""


def finding(kind, key, pos="lycaon/internal/store/store.go:3:6"):
    return {"kind": kind, "key": key, "pos": pos, "message": f"{kind} {key}"}


class ArchLintChangesTest(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.repo = self.tmp / "repo"
        (self.repo / "lycaon" / "internal" / "store").mkdir(parents=True)
        (self.repo / "lycaon-den" / "node_modules").mkdir(parents=True)
        (self.repo / "lycaon-den" / "package.json").write_text("{}")
        (self.repo / "fake_knip.py").write_text(FAKE_KNIP)
        (self.repo / "lycaon" / "internal" / "store" / "store.go").write_text("package store\n")
        (self.repo / "lycaon" / "go.mod").write_text("module x\n\ngo 1.22\n")
        self.git("init", "-q", "-b", "main")
        self.write_findings({"reach": [finding("dead-func", "x/store.Old")]})
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "base")
        self.base = self.git("rev-parse", "HEAD").strip()
        self.binary = self.executable("arch-lint", FAKE_ARCH_LINT)
        self.linter = self.executable("golangci-lint", FAKE_LINTER)
        self.cache = self.tmp / "cache"
        self.cache.mkdir()

    def git(self, *args):
        return subprocess.run(["git", "-C", str(self.repo), *args], check=True, capture_output=True, text=True).stdout

    def executable(self, name, body):
        path = self.tmp / name
        path.write_text(body)
        path.chmod(0o755)
        return path

    def write_findings(self, data):
        (self.repo / "findings.json").write_text(json.dumps(data))

    def test_fails_only_on_findings_the_base_lacks(self):
        self.write_findings({
            "reach": [finding("dead-func", "x/store.Old"), finding("dead-func", "x/store.New")],
            "refs": [finding("unreferenced-script", "scripts/old.sh", "scripts/old.sh")],
        })
        scope = change_report.Scope(self.base, "test base", ["findings.json", "lycaon/internal/store/store.go"])
        header, findings, failed = arch_lint_changes.changes(
            self.repo, scope, POLICY, self.cache, self.binary, self.linter)
        self.assertTrue(failed)
        self.assertIn("3 finding(s) at the working tree, 1 at the base", header)
        self.assertEqual([(f.area, f.path, f.line) for f in findings], [
            ("dead-func", "lycaon/internal/store/store.go", 3),
            ("unreferenced-script", "scripts/old.sh", None),
            ("unparam", "lycaon/internal/store/save.go", 7),
        ])
        # The base result is cached by commit and binary.
        self.assertEqual(len(list(self.cache.glob("base-*.json"))), 1)
        again = arch_lint_changes.base_checks(self.repo, self.base, self.binary, POLICY, self.cache)
        self.assertEqual(list(again), [("dead-func", "x/store.Old")])

    def test_a_change_without_go_runs_only_cross_language_checks(self):
        self.write_findings({
            "reach": [finding("dead-func", "x/store.New")],
            "refs": [finding("unreferenced-script", "scripts/old.sh", "scripts/old.sh")],
        })
        scope = change_report.Scope(self.base, "test base", ["findings.json", "scripts/old.sh"])
        _, findings, failed = arch_lint_changes.changes(self.repo, scope, POLICY, self.cache, self.binary, self.linter)
        self.assertTrue(failed)
        self.assertEqual([f.area for f in findings], ["unreferenced-script"])
        self.assertTrue(list(self.cache.glob("base-*-nogo.json")))

    def test_changed_go_packages_skip_tests_testdata_and_removed_packages(self):
        self.assertEqual(arch_lint_changes.changed_go_packages(self.repo, [
            "lycaon/internal/store/store.go", "lycaon/internal/store/store_test.go",
            "lycaon/internal/store/testdata/x.go", "lycaon/internal/gone/gone.go", "docs/x.go",
        ]), ["./internal/store"])

    def test_host_packages_skip_packages_the_host_does_not_build(self):
        darwin = self.repo / "lycaon" / "internal" / "mac"
        darwin.mkdir()
        (darwin / "mac.go").write_text("//go:build darwin\n\npackage mac\n")
        dirs = ["./internal/mac", "./internal/store"]
        self.assertEqual(arch_lint_changes.host_packages(self.repo, dirs, "linux"), ["./internal/store"])
        self.assertEqual(arch_lint_changes.host_packages(self.repo, dirs, "darwin"), dirs)
        self.assertEqual(arch_lint_changes.host_packages(self.repo, []), [])

    def test_den_changes_compare_knip_findings_with_the_base(self):
        (self.repo / "knip.json").write_text(json.dumps({"files": ["src/old.ts"]}))
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "knip base")
        base = self.git("rev-parse", "HEAD").strip()
        (self.repo / "knip.json").write_text(json.dumps({
            "files": ["src/old.ts", "src/new.ts"],
            "issues": [{"file": "src/a.ts", "exports": [{"name": "helper", "line": 3}], "types": [{"name": "Shape"}]}],
        }))
        (self.repo / "lycaon-den" / "knip.json").write_text("{}")
        scope = change_report.Scope(base, "test base", ["knip.json", "lycaon-den/src/a.ts"])
        with mock.patch.object(arch_lint_changes, "ROOT", self.repo):
            _, findings, failed = arch_lint_changes.den_changes(self.repo, scope, POLICY, self.cache)
            _, go_findings, _ = arch_lint_changes.changes(self.repo, scope, POLICY, self.cache, self.binary, self.linter)
        self.assertTrue(failed)
        self.assertFalse(any(f.area.startswith("unused-") for f in go_findings))
        self.assertEqual(sorted((f.area, f.path, f.line) for f in findings), [
            ("unused-export", "lycaon-den/src/a.ts", 3),
            ("unused-file", "lycaon-den/src/new.ts", None),
            ("unused-type", "lycaon-den/src/a.ts", 0),
        ])
        base_den = self.cache / "base-tree" / "lycaon-den"
        self.assertTrue((base_den / "node_modules").is_symlink())
        self.assertTrue((base_den / "knip.json").exists())

    def test_reuses_the_base_tree_for_a_new_base(self):
        arch_lint_changes.base_checks(self.repo, self.base, self.binary, POLICY, self.cache)
        self.write_findings({})
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "next")
        later = self.git("rev-parse", "HEAD").strip()
        self.assertEqual(arch_lint_changes.base_checks(self.repo, later, self.binary, POLICY, self.cache), {})

    def test_reach_receives_the_emitter_spec_the_policy_names(self):
        policy = {**POLICY, "reach": {**POLICY["reach"], "product": ["./cmd/app"]}, "emitters": {"registry": "r.json"}}
        arch_lint_changes.checks(self.binary, self.repo, policy)
        args = json.loads((self.repo / "args-reach.json").read_text())
        spec = args[args.index("-emitters") + 1]
        self.assertEqual(json.loads(spec), {"registry": "r.json"})
        self.assertEqual(args[args.index("-product") + 1], "./cmd/app")
        self.assertEqual(args[args.index("-repo") + 1], str(self.repo))
        arch_lint_changes.checks(self.binary, self.repo, POLICY)
        self.assertNotIn("-emitters", json.loads((self.repo / "args-reach.json").read_text()))

    def test_target_platforms_follow_the_host(self):
        reach = {"goos": {"darwin": ["darwin", "linux"], "linux": ["linux"]}}
        self.assertEqual(arch_lint_changes.target_goos(reach, "darwin"), ["darwin", "linux"])
        self.assertEqual(arch_lint_changes.target_goos(reach, "linux"), ["linux"])
        with self.assertRaises(RuntimeError):
            arch_lint_changes.target_goos(reach, "win32")

    def test_all_lists_every_finding_without_failing(self):
        header, findings, failed = arch_lint_changes.everything(self.repo, POLICY, self.binary)
        self.assertFalse(failed)
        self.assertEqual(header, "every finding at the working tree: dead-func 1")
        self.assertEqual([f.level for f in findings], ["notice"])

    def test_a_failing_tool_stops_the_check(self):
        broken = self.executable("broken", "#!/bin/sh\necho boom >&2\nexit 2\n")
        with self.assertRaisesRegex(RuntimeError, "boom"):
            arch_lint_changes.checks(broken, self.repo, POLICY)
        with self.assertRaisesRegex(RuntimeError, "golangci-lint"):
            arch_lint_changes.unparam(self.repo, self.base, broken, ["./internal/store"])
        self.assertEqual(arch_lint_changes.unparam(self.repo, self.base, broken, []), [])

    def test_den_check_passes_a_change_outside_den(self):
        scope = change_report.Scope(self.base, "test base", ["lycaon/internal/store/store.go"])
        header, findings, failed = arch_lint_changes.den_changes(self.repo, scope, POLICY, self.cache)
        self.assertEqual((findings, failed), ([], False))
        self.assertIn("no Den change", header)

    def test_main_measures_against_the_change_base(self):
        self.write_findings({"reach": [finding("dead-func", "x/store.Old"), finding("dead-func", "x/store.New")]})
        (self.repo / "lycaon" / "internal" / "store" / "store.go").write_text("package store\n\nfunc New() {}\n")
        with mock.patch.object(arch_lint_changes, "ROOT", self.repo), \
                mock.patch.object(arch_lint_changes, "build", return_value=self.binary), \
                mock.patch.object(arch_lint_changes, "golangci_lint", return_value=self.linter), \
                mock.patch.object(arch_lint_changes, "artifact_root", return_value=self.tmp / "artifacts"), \
                mock.patch.dict(os.environ, {"PW_CHANGE_BASE": self.base}), \
                mock.patch("sys.stdout"):
            policy = self.tmp / "policy.json"
            policy.write_text(json.dumps(POLICY))
            with mock.patch.object(arch_lint_changes, "POLICY", policy):
                self.assertEqual(arch_lint_changes.main([]), 1)
                self.assertEqual(arch_lint_changes.main(["--den"]), 0)
                self.assertEqual(arch_lint_changes.main(["--all"]), 0)

    def test_main_needs_a_base(self):
        with mock.patch.object(arch_lint_changes, "build", return_value=self.binary), \
                mock.patch.object(arch_lint_changes, "artifact_root", return_value=self.tmp / "artifacts"), \
                mock.patch.object(change_report, "resolve", return_value=None):
            with self.assertRaisesRegex(RuntimeError, "PW_CHANGE_BASE"):
                arch_lint_changes.main([])


if __name__ == "__main__":
    unittest.main()
