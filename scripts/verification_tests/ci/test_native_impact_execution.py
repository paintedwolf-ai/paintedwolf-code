"""Native admission preserves callers, contracts, and conservative fallback."""
import json
import unittest
from unittest.mock import patch

from ci_policy import hosted_plan, impact
from ci_policy.native_impact import dependencies


class NativeImpactTests(unittest.TestCase):
    def lanes(self, *paths):
        full, reasons = impact.broad_scope(paths)
        return {row["lane"] for row in hosted_plan.matrix(
            "integration", scope=dict(full=full, paths=list(paths), reasons=reasons))["include"]}

    def test_native_source_selects_its_dependencies_and_the_repository_baseline(self):
        baseline = {"limits", "contracts", "integration-build", "behavior"}
        cases = {
            "lycaon-den/src-tauri/src/lib.rs": {"native", "frontend"},
            "lycaon/internal/decide/native/src/main.rs": {"native"},
            "lycaon/internal/documentcore/native/src/main.rs": {"native", "frontend"},
        }
        for path, additional in cases.items():
            with self.subTest(path=path):
                self.assertEqual(impact.broad_scope([path]), (False, []))
                self.assertEqual(self.lanes(path), baseline | additional)

    def test_manifests_fixtures_build_policy_and_unknown_rust_stay_broad(self):
        for path in ["lycaon-den/src-tauri/Cargo.toml", "lycaon-den/src-tauri/Cargo.lock",
                     "lycaon-den/src-tauri/build.rs", "lycaon-den/src-tauri/tests/fixture.rs",
                     "lycaon/internal/decide/native/Cargo.toml", "lycaon/internal/documentcore/native/Cargo.lock",
                     "lycaon/internal/decide/native/src/weights.bin", "new/native/src/lib.rs",
                     "packaging/native/manifest.json", "scripts/build-decide.sh"]:
            with self.subTest(path=path):
                self.assertIsNone(dependencies(path))
                self.assertTrue(impact.broad_scope([path])[0])
                self.assertIn("build", self.lanes(path))
                self.assertIn("confinement-arm64", self.lanes(path))

    def test_mixed_changes_union_native_go_and_den_obligations(self):
        lanes = self.lanes("lycaon/internal/decide/native/src/main.rs", "lycaon/internal/a/a.go",
                           "lycaon-den/src/index.tsx")
        self.assertLessEqual({"native", "lint", "vulnerabilities", "frontend", "webkit-gtk"}, lanes)
        self.assertNotIn("confinement-arm64", lanes)
        self.assertIn("confinement-arm64", self.lanes("lycaon/internal/exec/supervisor_linux.go",
                                                     "lycaon/internal/documentcore/native/src/main.rs"))

    def graph(self):
        module = "github.com/lycaon/lycaon/"
        edges = {
            "internal/decide/bialy": [], "internal/documentcore": [],
            "internal/consumer": ["internal/decide/bialy"],
            "internal/testconsumer": ["internal/consumer"], "internal/unrelated": [],
            "test/contract/authority": [], "test/smoke": [],
        }
        return "\n".join(json.dumps({"ImportPath": module + directory,
                                     "Dir": str(impact.ROOT / "lycaon" / directory),
                                     "TestImports": [module + dependency for dependency in dependencies]})
                         for directory, dependencies in edges.items())

    def test_decision_source_reaches_adapter_and_its_transitive_test_callers(self):
        result = impact.subprocess.CompletedProcess([], 0, stdout=self.graph())
        scope = dict(full=False, paths=["lycaon/internal/decide/native/src/model.rs"])
        with patch.object(impact.subprocess, "run", return_value=result):
            self.assertEqual(impact.go_scope(scope), ["./internal/consumer", "./internal/decide/bialy",
                                                     "./internal/testconsumer", "./test/contract/authority", "./test/smoke"])

    def test_document_core_selects_its_go_dependencies_without_unrelated_packages(self):
        result = impact.subprocess.CompletedProcess([], 0, stdout=self.graph())
        with patch.object(impact.subprocess, "run", return_value=result):
            self.assertEqual(impact.go_scope(dict(full=False, paths=[
                "lycaon/internal/documentcore/native/src/main.rs"])),
                ["./internal/documentcore", "./test/contract/authority", "./test/smoke"])

    def test_unresolved_native_dependencies_or_graph_runs_the_full_go_recipe(self):
        for output, code in [("", 0), (self.graph(), 1), (self.graph().replace("internal/decide", "internal/missing"), 0)]:
            with self.subTest(output=output[:30], code=code), patch.object(
                    impact.subprocess, "run", return_value=impact.subprocess.CompletedProcess([], code, stdout=output)):
                self.assertIsNone(impact.go_scope(dict(full=False, paths=[
                    "lycaon/internal/decide/native/src/main.rs"])))

    def test_tauri_source_keeps_contracts_and_smoke_without_seeding_go(self):
        result = impact.subprocess.CompletedProcess([], 0, stdout=self.graph())
        with patch.object(impact.subprocess, "run", return_value=result):
            self.assertEqual(impact.go_scope(dict(full=False, paths=["lycaon-den/src-tauri/src/lib.rs"])),
                             ["./test/contract/authority", "./test/smoke"])
