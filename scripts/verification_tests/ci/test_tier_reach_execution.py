"""The tier check finds every Go test no tier runs, and only those."""
import copy
import json
from pathlib import Path
import tempfile
import textwrap
import unittest
from unittest.mock import patch

from ci_policy import tier_reach
from ci_policy.tier_reach import runs as tier_runs
from ci_policy.tier_reach.conditions import Index
from ci_policy.tier_reach.expression import Expression, evaluate
from ci_policy.tier_reach.source import name_matches, parse_file, satisfied, tokens
import verification_plan as planning

MODULE = "example.test/module"

SOURCES = {
    "go.mod": f"module {MODULE}\n",
    "gate/gate.go": """
        package gate

        import (
        	"os"
        	"strings"
        	"testing"
        )

        const EnvRequired = "EXAMPLE_REQUIRED"

        func required() bool {
        	v := strings.TrimSpace(strings.ToLower(os.Getenv(EnvRequired)))
        	return v == "1"
        }

        // Require skips without the tool unless the lane promised it.
        func Require(t testing.TB, present bool) {
        	t.Helper()
        	if present {
        		return
        	}
        	if required() {
        		t.Fatal("promised")
        	}
        	t.Skip("absent")
        }
        """,
    "pkg/pkg_test.go": """
        package pkg

        import (
        	"os"
        	"os/exec"
        	"runtime"
        	"testing"

        	"example.test/module/gate"
        )

        const envFixture = "EXAMPLE_FIXTURE"

        func TestPlain(t *testing.T) {}

        func TestEnvGated(t *testing.T) {
        	if v, ok := os.LookupEnv("EXAMPLE_GATE"); !ok || v == "" {
        		t.Skip("set EXAMPLE_GATE")
        	}
        }

        func TestShortGated(t *testing.T) {
        	if testing.Short() {
        		t.Skip("slow")
        	}
        }

        func TestLinuxOnlyBySwitch(t *testing.T) {
        	switch runtime.GOOS {
        	case "linux":
        	default:
        		t.Skip("linux only")
        	}
        }

        func TestRequiredTool(t *testing.T) {
        	_, err := exec.LookPath("tool")
        	gate.Require(t, err == nil)
        }

        func TestReturnsEarly(t *testing.T) {
        	if os.Getenv("EXAMPLE_RETURN") != "1" {
        		return
        	}
        }

        func TestHelperChild(t *testing.T) {
        	if os.Getenv(envFixture) != "1" {
        		t.Skip("helper process")
        	}
        }

        func TestStartsHelper(t *testing.T) {
        	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperChild$")
        	cmd.Env = append(os.Environ(), envFixture+"=1")
        	_ = cmd.Run()
        }

        func TestSubtestSkipsOnly(t *testing.T) {
        	t.Run("inner", func(t *testing.T) { t.Skip("inner only") })
        }

        func TestRaw(t *testing.T) {
        	_ = `
        }
        func TestNotATest(t *testing.T) {
        `
        }
        """,
    "pkg/darwin_only_test.go": """
        //go:build darwin

        package pkg

        import "testing"

        func TestDarwinOnly(t *testing.T) {}
        """,
    "pkg/tagged_test.go": """
        //go:build integration && !race

        package pkg

        import "testing"

        func TestIntegrationOnly(t *testing.T) {}
        """,
    "pkg/manual_windows_test.go": """
        package pkg

        import "testing"

        func TestWindowsOnly(t *testing.T) {}
        """,
}


def run(label, goos="linux", tags=(), short=False, environment=None, packages=("./...",), name=None):
    return tier_runs.Run(label, goos, "amd64", frozenset(tags), short, dict(environment or {}), list(packages),
                         "", name, None, MODULE)


class TierReachTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        for name, text in SOURCES.items():
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(textwrap.dedent(text).lstrip())
        self.index = Index(self.root, MODULE)

    def verdicts(self, runs, tags=(), named=()):
        findings = tier_reach.unreached(self.index, runs, set(tags), set(named))
        return {finding.split(":", 2)[1]: finding.split(":", 2)[2] for finding in findings}

    def test_each_gate_holds_a_test_back_until_some_run_satisfies_it(self):
        short = run("short", short=True)
        found = self.verdicts([short])
        self.assertEqual(set(found), {"TestEnvGated", "TestShortGated", "TestReturnsEarly", "TestDarwinOnly",
                                      "TestIntegrationOnly", "TestWindowsOnly"})
        self.assertIn("skips it", found["TestShortGated"])
        self.assertIn("darwin", found["TestDarwinOnly"])
        everything = [short,
                      run("full", tags=("integration",), environment={"EXAMPLE_GATE": "x", "EXAMPLE_RETURN": "1"}),
                      run("mac", goos="darwin")]
        self.assertEqual(set(self.verdicts(everything)), {"TestWindowsOnly"})

    def test_platform_switch_and_raced_tags_are_judged_per_run(self):
        self.assertIn("TestLinuxOnlyBySwitch", self.verdicts([run("mac", goos="darwin")]))
        self.assertNotIn("TestLinuxOnlyBySwitch", self.verdicts([run("linux")]))
        self.assertIn("TestIntegrationOnly", self.verdicts([run("race", tags=("integration", "race"))]))

    def test_a_promised_requirement_counts_as_reached_and_name_filters_apply(self):
        selective = run("selected", name="^TestRequiredTool$", environment={"EXAMPLE_REQUIRED": "1"})
        found = self.verdicts([selective])
        self.assertNotIn("TestRequiredTool", found)
        self.assertIn("-run", found["TestPlain"])
        self.assertNotIn("TestHelperChild", found, "a helper process is reached with the test that starts it")
        self.assertNotIn("TestSubtestSkipsOnly", self.verdicts([run("plain")]))

    def test_declared_exemptions_cover_tags_and_names_and_go_stale(self):
        everything = [run("full", tags=("integration",), environment={"EXAMPLE_GATE": "x", "EXAMPLE_RETURN": "1"}),
                      run("mac", goos="darwin")]
        windows = "pkg/manual_windows_test.go:TestWindowsOnly"
        self.assertEqual(self.verdicts(everything, named={windows}), {})
        stale = self.verdicts(everything, named={windows, "pkg/pkg_test.go:TestPlain", "pkg/gone_test.go:TestGone"})
        self.assertIn("declared not run, yet", stale["TestPlain"])
        self.assertIn("no such test", stale["TestGone"])
        self.assertNotIn("TestIntegrationOnly", self.verdicts([run("plain")], tags={"integration"}, named={windows}))

    def test_source_reading_follows_gofmt_layout_and_build_rules(self):
        parsed = parse_file(self.root / "pkg/pkg_test.go", "pkg")
        names = [f.name for f in parsed.functions]
        self.assertIn("TestRaw", names)
        self.assertNotIn("TestNotATest", names, "a raw string never opens a declaration")
        self.assertEqual(parsed.constants["envFixture"], "EXAMPLE_FIXTURE")
        self.assertEqual(parsed.imports["gate"], f"{MODULE}/gate")
        self.assertTrue(satisfied("(linux || darwin) && !race && go1.26", {"linux"}))
        self.assertFalse(satisfied("linux && cgo", {"linux"}))
        self.assertTrue(name_matches("x_linux_arm64_test.go", "linux", "arm64"))
        self.assertFalse(name_matches("x_windows.go", "linux", "amd64"))
        self.assertTrue(name_matches("linux.go", "darwin", "arm64"), "the first element never constrains")
        braces = [t.text for t in tokens('x := "{"\ny := 1\n')]
        self.assertEqual(braces.count("{"), 0)
        self.assertEqual(braces.count(";"), 2)

    def test_conditions_decide_only_what_a_run_fixes(self):
        files = {f.path.name: f for f in self.index.files}
        lane = run("linux", environment={"EXAMPLE_GATE": "YES", "EXAMPLE_REQUIRED": "1", "UNSET_VALUE": None})
        cases = [
            ("pkg_test.go", 'runtime.GOARCH == "arm64"', {}, False),
            ("pkg_test.go", 'runtime.GOOS != "darwin" && !testing.Short()', {}, True),
            ("pkg_test.go", 'gate.EnvRequired == "EXAMPLE_REQUIRED"', {}, True),
            ("pkg_test.go", 'os.Getenv(envFixture) == ""', {}, True),
            ("pkg_test.go", 'os.Getenv("UNSET_VALUE") == "1"', {}, None),
            ("gate.go", 'strings.ToLower(os.Getenv("EXAMPLE_GATE")) == "yes"', {}, True),
            ("gate.go", 'required() || false', {}, True),
            ("gate.go", '!(true && false) || os.Getenv("MISSING") != ""', {}, True),
            ("gate.go", 'Require(t, true)', {}, None),
            ("gate.go", 'count > 3', {}, None),
            ("gate.go", 'a + b == ""', {}, None),
            ("gate.go", '-limit == 0', {}, None),
            ("gate.go", 'items[0] == "a"', {}, None),
            ("gate.go", 'value.Field.Method() == "a"', {}, None),
            ("gate.go", '(os.Getenv("X") == "1"', {}, None),
            ("gate.go", 'os.Getenv(1) == ""', {}, None),
            ("gate.go", '42 == 42', {}, None),
            ("gate.go", 'loop == ""', {"loop": (None, tokens("loop"))}, None),
            ("gate.go", 'present', {"present": (1, tokens('os.LookupEnv("EXAMPLE_GATE")'))}, True),
            ("gate.go", 'value == "YES"', {"value": (0, tokens('os.LookupEnv("EXAMPLE_GATE")'))}, True),
            ("gate.go", 'strings.TrimSpace(other) == ""', {"other": (0, tokens("pair()"))}, None),
        ]
        for name, text, bindings, want in cases:
            with self.subTest(text=text):
                tree = Expression(self.index, files[name], bindings).parse(tokens(text))
                self.assertEqual(evaluate(tree, lane), want)

    def test_runs_select_packages_names_and_scripted_digests(self):
        selected = tier_runs.Run("r", "linux", "amd64", frozenset(), False, {}, ["./..."],
                                 r"/test/(contract)(/|$)", "^(TestA|TestB)$/sub", None, MODULE)
        self.assertTrue(selected.selects_package("internal/x"))
        self.assertFalse(selected.selects_package("test/contract"))
        self.assertTrue(selected.selects_name("TestB"))
        self.assertFalse(selected.selects_name("TestC"))
        shell = tier_runs.digest_calls(
            'A=1 bash "${ROOT}/scripts/go-test-digest.sh" --full --tags x --name n -- ./a ./b -run \'^T$\' -count=1\n'
            'bash s/with-bundled-scanners.sh -- bash s/go-test-digest.sh --name m -- ./c -tags=y\n')
        self.assertEqual(shell[0], (frozenset({"x"}), False, {"A": "1"}, ["./a", "./b"], "^T$"))
        self.assertEqual(shell[1][0], frozenset({"y"}))
        self.assertEqual(shell[1][2]["LYCAON_SCANNER_SUITE_REQUIRED"], "1")
        literal = tier_runs.digest_calls('[ "go-test-digest.sh", "--tags", "z", "--", "-run", pattern, "./d"]',
                                         literal=True)
        self.assertEqual(literal, [(frozenset({"z"}), True, {}, ["./d"], None)])
        taskfile = {"outer": {"cmds": [{"task": "inner"}]},
                    "inner": {"cmds": ["bash {{.SCRIPTS_DIR}}/go-test-digest.sh --full --name i -- ./e"]}}
        self.assertEqual(tier_runs.scripted_runs("outer", taskfile)[0][3], ["./e"])

    def test_workflow_steps_and_lanes_become_runs_with_declared_environment(self):
        step = 'X=1 ./task test:digest -- ./internal/browser -run \'^TestB$\' -count=1 | tee log'
        self.assertEqual(list(tier_runs.task_invocations(step)),
                         [({"X": "1"}, ["test:digest"], ["./internal/browser", "-run", "^TestB$", "-count=1"])])
        workflows = {
            "ci.yml": {True: {"pull_request": None}, "jobs": {"p": {"uses": "./.github/workflows/platform.yml"}}},
            "platform.yml": {True: {"workflow_call": None}, "jobs": {"mac": {
                "runs-on": "macos-15", "env": {"UNDECLARED": "1", "PW_CONFINE_REQUIRED": "1"},
                "steps": [{"name": "go", "run": step}]}}},
            "manual.yml": {True: {"workflow_dispatch": None}, "jobs": {}},
            "verification.yml": {True: {"workflow_call": None},
                                 "jobs": {"verify": {"env": {"PW_X": "${{ inputs.x }}"}}}},
        }
        self.assertEqual(tier_runs.tier_workflows(workflows), ["ci.yml", "platform.yml"])
        mac = tier_runs.workflow_runs(workflows, MODULE, {})
        self.assertEqual({(r.goos, r.run) for r in mac}, {("darwin", "^TestB$")})
        self.assertEqual(mac[0].environment, {"PW_CONFINE_REQUIRED": "1"}, "the scheduler drops undeclared names")
        lanes = tier_runs.lane_runs(workflows, MODULE, {})
        self.assertTrue(lanes)
        self.assertTrue(all(r.environment["PW_X"] is None for r in lanes), "an expression's value is unknown")

    def test_catalog_requires_a_reason_for_every_exemption(self):
        good = copy.deepcopy(planning.catalog())
        for mutate in (lambda c: c["not_run"]["tags"].update({"x": ""}),
                       lambda c: c["not_run"]["tests"].append({"go": ["a:TestA"]}),
                       lambda c: c["not_run"].pop("tags")):
            bad = copy.deepcopy(good)
            mutate(bad)
            with tempfile.TemporaryDirectory() as directory:
                path = Path(directory) / "verification-plan.json"
                path.write_text(json.dumps(bad))
                with patch.object(planning, "CATALOG", path), self.assertRaises(ValueError):
                    planning.catalog()

    def test_declared_rust_tests_must_exist(self):
        present = "lycaon-den/src-tauri/src/protected_launch/none.rs:non_unix_targets_are_never_protected"
        self.assertEqual(tier_reach.missing_rust({present}), [])
        self.assertEqual(len(tier_reach.missing_rust({present + "_gone"})), 1)


if __name__ == "__main__":
    unittest.main()
