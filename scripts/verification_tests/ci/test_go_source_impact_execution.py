"""Command and test source retain Go graph coverage without unrelated native jobs."""
import json
import unittest
from unittest.mock import patch

from ci_policy import hosted_plan, impact


class GoSourceImpactTests(unittest.TestCase):
    def test_go_source_roots_keep_the_backend_lanes(self):
        expected = {"limits", "contracts", "integration-build", "behavior", "lint", "vulnerabilities"}
        for path in ["lycaon/internal/a/a.go", "lycaon/cmd/engine/main.go",
                     "lycaon/test/contract/release/corpus_test.go", "lycaon/test/wiring/host_test.go",
                     "lycaon/test/security/scan_test.go", "lycaon/test/contract/snapshot_test.go"]:
            with self.subTest(path=path):
                full, reasons = impact.broad_scope([path])
                self.assertEqual((full, reasons), (False, []))
                rows = hosted_plan.matrix("integration", scope=dict(full=full, paths=[path]))["include"]
                self.assertEqual({row["lane"] for row in rows}, expected)

    def test_data_public_packages_and_unknown_roots_still_run_the_whole_gate(self):
        for path in ["lycaon/test/contract/release/testdata/manifest.json", "lycaon/test/fixtures/go.mod",
                     "lycaon/cmd/engine/assets/config.yaml", "lycaon/pkg/api/types.go",
                     "lycaon/new/code.go", "scripts/verification_tests/ci/test_plan.py"]:
            with self.subTest(path=path):
                self.assertEqual(impact.broad_scope([path]), (True, [path]))

    def test_go_outside_the_module_graph_runs_the_whole_gate(self):
        # Harness projects are nested modules other lanes copy and run; Go ignores testdata and _ or . directories.
        for path in ["lycaon/test/fixtures/e2e/minimal-go-project/internal/app/app.go",
                     "lycaon/test/fixtures/e2e/hostile-project/main.go", "lycaon/test/testdata/scan/vuln.go",
                     "lycaon/internal/confine/testdata/probe/main.go", "lycaon/internal/a/_scratch/a.go",
                     "lycaon/cmd/tool/.cache/a.go"]:
            with self.subTest(path=path):
                self.assertEqual(impact.broad_scope([path]), (True, [path]))
        self.assertEqual(impact.broad_scope(["lycaon/test/wiring/fixtures/coordinator_batch/fixture.go"]),
                         (False, []))

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
        self.assertLessEqual({"behavior", "native", "frontend", "webkit-gtk", "lint", "vulnerabilities"}, selected)
