"""Command and test source retain Go graph coverage without unrelated native jobs."""
import json
import unittest
from unittest.mock import patch

from ci_policy import hosted_plan, impact


class GoSourceImpactTests(unittest.TestCase):
    def test_go_source_roots_keep_the_backend_lanes(self):
        for path in ["lycaon/internal/a/a.go", "lycaon/cmd/engine/main.go",
                     "lycaon/test/contract/release/corpus_test.go", "lycaon/test/wiring/host_test.go",
                     "lycaon/test/security/scan_test.go", "lycaon/test/contract/snapshot_test.go"]:
            with self.subTest(path=path):
                full, reasons = impact.broad_scope([path])
                self.assertEqual((full, reasons), (False, []))
                self.assertEqual(impact.change_areas(dict(full=full, paths=[path])), {"always", "go"})

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
        self.assertEqual(impact.change_areas(dict(full=False, paths=["docs/git.md"])), {"always"})
        for path in ["scripts/verification_tests/ci/test_plan.py", "scripts/ci_policy/health.py",
                     ".github/workflows/nightly.yml"]:
            with self.subTest(path=path):
                self.assertEqual(impact.broad_scope([path]), (False, []))
                self.assertEqual(impact.change_areas(dict(full=False, paths=[path])), {"always", "scripts"})

    def test_a_broad_change_runs_the_script_suites_only_when_it_touches_what_they_read(self):
        lanes = hosted_plan.lanes()
        scripts_only = {name for name, lane in lanes.items() if lane.get("changes") == ["scripts"]}
        self.assertTrue(scripts_only)
        for paths in (["lycaon/config/prompt.md"], ["lycaon/pkg/api/types.go", "docs/openapi/paths.yaml"]):
            with self.subTest(paths=paths):
                self.assertEqual(impact.change_areas(dict(full=True, paths=paths)), hosted_plan.AREAS - {"scripts"})
                rows = hosted_plan.matrix("integration", scope=dict(full=True, paths=paths))["include"]
                self.assertFalse(scripts_only & {row["lane"] for row in rows})
        for paths in ([], ["scripts/verification-plan.json"], ["packaging/nix/flake.nix", "lycaon/config/prompt.md"],
                      ["Taskfile.yml"], ["task"]):
            with self.subTest(paths=paths):
                self.assertEqual(impact.change_areas(dict(full=True, paths=paths)), hosted_plan.AREAS)

    def test_a_pull_request_runs_the_den_suite_only_when_its_inputs_change(self):
        def fast(paths, full=False):
            return {row["lane"] for row in hosted_plan.matrix("fast", scope=dict(full=full, paths=paths))["include"]}
        fast_lanes = {name: lane for name, lane in hosted_plan.lanes().items() if "fast" in lane["profiles"]}
        every = set(fast_lanes)
        # A lane selected only for Den changes waits for them.
        go = {name for name, lane in fast_lanes.items() if "always" in lane["changes"] or "den" not in lane["changes"]}
        for paths in (["lycaon-den/src/components/card.tsx"], ["docs/openapi/paths.yaml"], ["CHANGELOG.md"],
                      [".bun-version"], ["lycaon/internal/a/a.go", "lycaon-den/package.json"]):
            with self.subTest(paths=paths):
                self.assertEqual(fast(paths, impact.broad_scope(paths)[0]), every)
        for paths in (["lycaon/internal/a/a.go"], ["scripts/ci_policy/health.py"], ["lycaon/config/prompt.md"]):
            with self.subTest(paths=paths):
                self.assertEqual(fast(paths, impact.broad_scope(paths)[0]), go)
        self.assertEqual(fast([], full=True), every)
        self.assertLess(go, every)

    def test_a_pull_request_of_prose_alone_leaves_the_static_job_as_its_only_check(self):
        for paths in (["docs/git.md"], ["README.md", "lycaon/AGENTS.md", "docs/operations/release.md"]):
            with self.subTest(paths=paths):
                self.assertEqual(hosted_plan.matrix("fast", scope=dict(full=False, paths=paths)), {"include": []})
                # The merge queue still runs its own lanes for the same change.
                self.assertTrue(hosted_plan.matrix("integration", scope=dict(full=False, paths=paths))["include"])
        for paths in (["docs/git.md", "lycaon/internal/a/a.go"], ["docs/openapi/paths.yaml"], ["CHANGELOG.md"]):
            with self.subTest(paths=paths):
                self.assertFalse(impact.docs_only(paths))
                self.assertTrue(hosted_plan.matrix("fast", scope=dict(full=impact.broad_scope(paths)[0], paths=paths))["include"])

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
        self.assertEqual(impact.change_areas(dict(full=False, paths=paths)), {"always", "go", "den", "decide-native"})
