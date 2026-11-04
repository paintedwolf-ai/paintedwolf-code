"""Command and test source retain Go graph coverage without unrelated native jobs."""
import json
import unittest
from unittest.mock import patch

from ci_policy import hosted_plan, impact


class GoSourceImpactTests(unittest.TestCase):
    def test_go_source_roots_keep_the_backend_lanes(self):
        expected = {"limits", "contracts", "integration-build", "behavior", "lint", "vulnerabilities", "command-scale"}
        for path in ["lycaon/internal/a/a.go", "lycaon/cmd/engine/main.go",
                     "lycaon/test/contract/release/corpus_test.go", "lycaon/test/wiring/host_test.go",
                     "lycaon/test/security/scan_test.go", "lycaon/test/contract/snapshot_test.go"]:
            with self.subTest(path=path):
                full, reasons = impact.broad_scope([path])
                self.assertEqual((full, reasons), (False, []))
                rows = hosted_plan.matrix("integration", scope=dict(full=full, paths=[path]))["include"]
                self.assertEqual({row["lane"] for row in rows}, expected)

    def test_public_packages_and_unknown_roots_still_run_the_whole_gate(self):
        for path in ["lycaon/test/fixtures/go.mod", "lycaon/pkg/api/types.go", "lycaon/new/code.go",
                     "scripts/verification_execute.py", "scripts/verification-plan.json",
                     ".github/actions/setup-verification/action.yml", "lycaon/config/prompt.md"]:
            with self.subTest(path=path):
                self.assertEqual(impact.broad_scope([path]), (True, [path]))

    def test_nested_modules_run_the_whole_gate(self):
        # Harness projects are nested modules other lanes copy and run.
        for path in ["lycaon/test/fixtures/e2e/minimal-go-project/internal/app/app.go",
                     "lycaon/test/fixtures/e2e/hostile-project/main.go"]:
            with self.subTest(path=path):
                self.assertEqual(impact.broad_scope([path]), (True, [path]))

    def test_ci_tooling_adds_only_its_own_lane(self):
        baseline = {"limits", "contracts", "integration-build", "behavior"}
        for path in ["scripts/verification_tests/ci/test_plan.py", "scripts/ci_policy/health.py",
                     ".github/workflows/nightly.yml"]:
            with self.subTest(path=path):
                self.assertEqual(impact.broad_scope([path]), (False, []))
                lanes = impact.selected_lanes(dict(full=False, paths=[path]), hosted_plan.lanes())
                self.assertEqual(lanes & (baseline | {"runner", "lint", "frontend", "native"}), baseline & set(hosted_plan.lanes()) | {"runner"})

    def test_files_that_run_every_lane_widen(self):
        for path in [".github/workflows/ci.yml", ".github/workflows/verification.yml",
                     "scripts/ci_policy/hosted_execution.py", "scripts/ci_policy/hosted_plan.py"]:
            with self.subTest(path=path):
                self.assertEqual(impact.broad_scope([path]), (True, [path]))

    def test_data_files_seed_their_nearest_package(self):
        directories = {"lycaon/internal/a": "module/internal/a", "lycaon/internal/a/b": "module/internal/a/b"}
        self.assertEqual(impact.owning_package("lycaon/internal/a/testdata/deep/x.json", directories), "module/internal/a")
        self.assertEqual(impact.owning_package("lycaon/internal/a/b/assets.yaml", directories), "module/internal/a/b")
        self.assertIsNone(impact.owning_package("lycaon/test/testdata/x.json", directories))
        for path in ["lycaon/internal/a/testdata/x.json", "lycaon/cmd/engine/assets/config.yaml"]:
            self.assertEqual(impact.broad_scope([path]), (False, []))

    def test_changed_test_support_reaches_importers_and_keeps_all_contracts_and_smoke(self):
        module = "github.com/lycaon/lycaon/"
        edges = {"test/support": {}, "test/security": {"TestImports": ["test/support"]},
                 "cmd/tool": {"XTestImports": ["test/support"]}, "internal/unrelated": {},
                 "test/contract/release": {}, "test/contract/hosted": {}, "test/smoke": {}}
        output = "\n".join(json.dumps({
            "ImportPath": module + directory, "Dir": str(impact.ROOT / "lycaon" / directory),
            **{kind: [module + dependency for dependency in dependencies]
               for kind, dependencies in imports.items()},
        }) for directory, imports in edges.items())
        result = impact.subprocess.CompletedProcess([], 0, stdout=output)
        with patch.object(impact.subprocess, "run", return_value=result):
            self.assertEqual(impact.go_scope(dict(full=False, paths=["lycaon/test/support/fixture.go"])),
                             sorted("./" + directory for directory in edges if directory != "internal/unrelated"))
            self.assertEqual(impact.go_scope(dict(full=False, paths=["lycaon/cmd/tool/main.go"])),
                             ["./cmd/tool", "./test/contract/hosted", "./test/contract/release", "./test/smoke"])
            for path in ["lycaon/test/deleted/old_test.go", "lycaon/cmd/deleted/main.go"]:
                with self.subTest(path=path):
                    self.assertIsNone(impact.go_scope(dict(full=False, paths=[path])))

    def test_mixed_changes_union_frontend_and_native_obligations(self):
        paths = ["lycaon/test/contract/release/corpus_test.go", "lycaon-den/src/app.tsx",
                 "lycaon/internal/decide/native/src/main.rs"]
        self.assertEqual(impact.broad_scope(paths), (False, []))
        selected = impact.selected_lanes(dict(full=False, paths=paths), hosted_plan.lanes())
        self.assertLessEqual({"behavior", "native", "frontend", "webkit-gtk", "lint", "vulnerabilities", "command-scale"}, selected)
