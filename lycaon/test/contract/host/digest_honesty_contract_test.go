package contract

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestDigestWrappersDoNotLoseInformation(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)

	vitestSh := filepath.Join(root, "scripts", "vitest-digest.sh")
	data, err := os.ReadFile(vitestSh)
	contractcheck.FailErr(t, "read vitest-digest.sh", err)
	text := string(data)

	if strings.Contains(text, "--outputFile=\"${TMP_RAW}\" >/dev/null") ||
		strings.Contains(text, "--outputFile=\"${TMP_RAW}\" \"${VITEST_ARGS[@]}\" >/dev/null") {
		t.Fatal("vitest console output must be captured, not sent to /dev/null — " +
			"it carries the reason for a non-zero exit that no test failure explains")
	}
	for _, want := range []string{"--vitest-rc", "--stdout-log"} {
		if !strings.Contains(text, want) {
			t.Fatalf("vitest-digest.sh must pass %s to the digest so a non-zero exit "+
				"with zero failing tests is not rendered green", want)
		}
	}

	if !strings.Contains(text, "--reporter=json") {
		t.Fatal("vitest-digest.sh must run the json reporter — the digest parses its report")
	}
	if !strings.Contains(text, `: >"${TMP_RAW}"`) {
		t.Fatal("vitest-digest.sh must publish the current empty report when the runner exits before writing JSON")
	}
	for _, want := range []string{"vitest-run-outcome-reporter.ts", "VITEST_OUTCOME_FILE", "--outcome"} {
		if !strings.Contains(text, want) {
			t.Fatalf("vitest-digest.sh must wire the outcome reporter and pass its record to "+
				"the digest (missing %q) — vitest's own unhandled errors are the authority "+
				"for a non-zero exit that no test failure explains", want)
		}
	}
	humanReporter := false
	for _, reporter := range []string{"--reporter=default", "--reporter=verbose", "--reporter=dot", "--reporter=basic"} {
		if strings.Contains(text, reporter) {
			humanReporter = true
			break
		}
	}
	if !humanReporter {
		t.Fatal("vitest-digest.sh must pair --reporter=json with a human reporter " +
			"(default/verbose/dot/basic); a hard crash writes no outcome record, and json " +
			"alone prints no diagnostics, so the run log is all that survives")
	}

	for _, name := range []string{"vitest-digest.sh", "go-test-digest.sh"} {
		data, err := os.ReadFile(filepath.Join(root, "scripts", name))
		contractcheck.FailErr(t, "read "+name, err)
		body := string(data)
		if strings.Contains(body, `)".json`) {
			t.Fatalf("%s appends .json to a mktemp result — the created stem is then never "+
				"removed; reserve it in TMP_STEM and clean both", name)
		}
		if !strings.Contains(body, "TMP_STEM") {
			t.Fatalf("%s must track the mktemp stem so cleanup removes it", name)
		}
		failureRoot := `FAIL_DIR="${LOGDIR}/failures"`
		if name == "go-test-digest.sh" {
			failureRoot = `FAILURE_REL="failures/go-test-`
		}
		for _, want := range []string{failureRoot, "retained:"} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s must retain its unique failure capture before publication (missing %q)", name, want)
			}
		}
		capture := `cp -f "${TMP_RAW}"`
		if name == "vitest-digest.sh" {
			capture = `"${TMP_RAW}:${FAIL_DIR}/den-test-`
		}
		if !strings.Contains(body, capture) {
			t.Fatalf("%s must retain TMP_RAW directly", name)
		}
		publication := "publish_captures"
		retention := `FAIL_DIR="${LOGDIR}/failures"`
		if name == "go-test-digest.sh" {
			publication = `digest_acquire_lock "${LOCKDIR}"`
			retention = `FAILURE_STAGE="${RUN_DIR}/failure"`
		}
		if strings.Index(body, retention) > strings.LastIndex(body, publication) {
			t.Fatalf("%s must stage failure evidence before publication", name)
		}
	}
}

func TestDigestWrappersIsolateAndReapTestRuns(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	helper := contractcheck.ReadRepoFile(t, root, "scripts/test-run-isolation.sh")
	for _, want := range []string{
		`TEST_RUN_ROOT="/tmp/`, "test_run_prune_abandoned", "test-run-lease.py", "test_run_create_isolation",
		`export HOME=`, `export TMPDIR=`, `export XDG_CACHE_HOME=`, `export XDG_CONFIG_HOME=`,
		`export XDG_DATA_HOME=`, `unset LYCAON_CONFIG_DIR`,
	} {
		if !strings.Contains(helper, want) {
			t.Fatalf("test-run-isolation.sh must isolate and reclaim test state (missing %q)", want)
		}
	}

	wrappers := []struct {
		name, create string
	}{
		{"go-test-digest.sh", `test_run_create_isolation "go-test"`},
		{"vitest-digest.sh", `test_run_create_isolation "den-test"`},
	}
	for _, wrapper := range wrappers {
		body := contractcheck.ReadRepoFile(t, root, "scripts/"+wrapper.name)
		for _, want := range []string{
			`source "${ISOLATION_SH}"`, wrapper.create, "test_run_export_isolation",
			`test_run_remove_isolation "${RUN_DIR}"`, `rmdir "${TEST_RUN_ROOT}"`, "reap_test_group",
			`kill -TERM -- "-${`, `kill -KILL -- "-${`, "process-group-watchdog.py", "reap_watchdog",
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s must isolate files and reap its process group (missing %q)", wrapper.name, want)
			}
		}
		if strings.Contains(body, "LYCAON_TEST_RUN_ROOT") {
			t.Fatalf("%s must use the shared short run root", wrapper.name)
		}
	}
}

func TestDigestRunLockSmoke(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "digest-run-lock-smoke.sh"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run digest lock smoke: %v\n%s", err, output)
	}
}

func TestVitestDigestReportsRunnerFailureWithNoFailingTests(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	digest := filepath.Join(root, "scripts", "vitest-digest.py")
	if _, err := os.Stat(digest); err != nil {
		t.Skipf("vitest-digest.py not present: %v", err)
	}

	dir := t.TempDir()
	report := filepath.Join(dir, "report.json")
	contractcheck.FailErr(t, "write report", os.WriteFile(report,
		[]byte(`{"numTotalTests":10,"numPassedTests":10,"testResults":[]}`), 0o600))
	console := filepath.Join(dir, "console.out")
	contractcheck.FailErr(t, "write console", os.WriteFile(console,
		[]byte("ok\n\nUnhandled Error\nError: leaked timer fired after teardown\n"), 0o600))

	run := func(rc string) (string, error) {
		cmd := exec.Command("python3", digest, "--name", "den:test", "--input", report,
			"--vitest-rc", rc, "--stdout-log", console, "--stdout-log-path", ".task/last-run/den-test.out")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := run("1")
	if err == nil {
		t.Fatalf("digest must exit non-zero when the runner failed:\n%s", out)
	}
	if strings.Contains(out, "✓") {
		t.Fatalf("digest must not render a tick when the runner failed:\n%s", out)
	}
	for _, want := range []string{"vitest exited 1", "Unhandled Error", "leaked timer fired after teardown", "den-test.out"} {
		if !strings.Contains(out, want) {
			t.Fatalf("digest output must mention %q so the failure is actionable:\n%s", want, out)
		}
	}

	out, err = run("0")
	if err != nil {
		t.Fatalf("digest must stay green when the runner succeeded: %v\n%s", err, out)
	}
	if !strings.Contains(out, "✓") || !strings.Contains(out, "10/10 tests passed") {
		t.Fatalf("digest must report a clean run as passing:\n%s", out)
	}
}

func TestVitestDigestExplainsFromVitestOwnOutcomeRecord(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	digest := filepath.Join(root, "scripts", "vitest-digest.py")
	if _, err := os.Stat(digest); err != nil {
		t.Skipf("vitest-digest.py not present: %v", err)
	}

	dir := t.TempDir()
	report := filepath.Join(dir, "report.json")
	contractcheck.FailErr(t, "write report", os.WriteFile(report,
		[]byte(`{"numTotalTests":10,"numPassedTests":10,"testResults":[]}`), 0o600))
	console := filepath.Join(dir, "console.out")
	contractcheck.FailErr(t, "write console", os.WriteFile(console, []byte("ok\nrun summary\n"), 0o600))

	run := func(outcome string) (string, error) {
		args := []string{digest, "--name", "den:test", "--input", report, "--vitest-rc", "1",
			"--stdout-log", console, "--stdout-log-path", ".task/last-run/den-test.out"}
		if outcome != "" {
			args = append(args, "--outcome", outcome)
		}
		out, err := exec.Command("python3", args...).CombinedOutput()
		return string(out), err
	}

	withErrors := filepath.Join(dir, "outcome.json")
	contractcheck.FailErr(t, "write outcome", os.WriteFile(withErrors, []byte(`{"reason":"passed",
	  "unhandledErrors":[{"name":"Error","message":"leaked timer fired after teardown",
	    "stack":"Error: leaked timer fired after teardown\n    at zap (src/a.ts:1:1)"}]}`), 0o600))
	out, err := run(withErrors)
	if err == nil {
		t.Fatalf("digest must exit non-zero when the runner failed:\n%s", out)
	}
	for _, want := range []string{"unhandled error", "leaked timer fired after teardown", "at zap (src/a.ts:1:1)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("digest must report the recorded unhandled error (missing %q):\n%s", want, out)
		}
	}

	out, err = run(filepath.Join(dir, "absent.json"))
	if err == nil {
		t.Fatalf("digest must still fail with no outcome record:\n%s", out)
	}
	if !strings.Contains(out, "run summary") {
		t.Fatalf("with no outcome record the digest must fall back to the console log:\n%s", out)
	}

	passedOnly := filepath.Join(dir, "passed.json")
	contractcheck.FailErr(t, "write outcome", os.WriteFile(passedOnly,
		[]byte(`{"reason":"passed","unhandledErrors":[]}`), 0o600))
	out, err = run(passedOnly)
	if err == nil {
		t.Fatalf("digest must fail when vitest exited non-zero:\n%s", out)
	}
	for _, want := range []string{"'passed'", "run summary"} {
		if !strings.Contains(out, want) {
			t.Fatalf("digest must state the reason and still show the log (missing %q):\n%s", want, out)
		}
	}
}

func TestVitestDigestReportsFileLevelFailures(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	digest := filepath.Join(root, "scripts", "vitest-digest.py")
	if _, err := os.Stat(digest); err != nil {
		t.Skipf("vitest-digest.py not present: %v", err)
	}

	dir := t.TempDir()
	report := filepath.Join(dir, "report.json")
	body := `{"numTotalTests":3757,"numPassedTests":3757,"numFailedTests":0,"success":false,
	  "testResults":[
	    {"name":"src/ok.test.ts","status":"passed","assertionResults":[{"status":"passed","title":"fine"}]},
	    {"name":"src/broken.test.ts","status":"failed","assertionResults":[],
	     "message":"Error: Cannot find module './missing'"}
	  ]}`
	contractcheck.FailErr(t, "write report", os.WriteFile(report, []byte(body), 0o600))

	for _, rc := range []string{"1", "0"} {
		cmd := exec.Command("python3", digest, "--name", "den:test", "--input", report, "--vitest-rc", rc)
		raw, err := cmd.CombinedOutput()
		out := string(raw)
		if err == nil {
			t.Fatalf("rc=%s: digest must fail when a test file failed to run:\n%s", rc, out)
		}
		if strings.Contains(out, "✓") {
			t.Fatalf("rc=%s: digest must not render a tick when a test file failed to run:\n%s", rc, out)
		}
		for _, want := range []string{"failed to run", "src/broken.test.ts", "Cannot find module"} {
			if !strings.Contains(out, want) {
				t.Fatalf("rc=%s: digest must mention %q:\n%s", rc, want, out)
			}
		}
		if strings.Contains(out, "3757/3757 tests passed") {
			t.Fatalf("rc=%s: digest must not headline the passing count on a red run:\n%s", rc, out)
		}
	}
}

func TestVitestDigestPrintsCompleteAssertionValues(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	digest := filepath.Join(root, "scripts", "vitest-digest.py")
	dir := t.TempDir()
	report := filepath.Join(dir, "report.json")
	outcome := filepath.Join(dir, "outcome.json")
	contractcheck.FailErr(t, "write report", os.WriteFile(report, []byte(`{
  "numTotalTests": 1,
  "numPassedTests": 0,
  "testResults": [{
    "name": "src/audit.test.ts",
    "status": "failed",
    "assertionResults": [{
      "status": "failed",
      "fullName": "audit reports violations",
      "failureMessages": ["AssertionError: expected [ Array(1) ] to deeply equal []"]
    }]
  }]
}`), 0o600))
	contractcheck.FailErr(t, "write outcome", os.WriteFile(outcome, []byte(`{
  "reason": "failed",
  "unhandledErrors": [],
  "failedTests": [{
    "file": "src/audit.test.ts",
    "name": "audit > reports violations",
    "errors": [{"actual": "[\n  'src/problem.ts:9'\n]", "expected": "[]"}]
  }]
}`), 0o600))

	cmd := exec.Command("python3", digest, "--name", "den:test", "--input", report, "--outcome", outcome)
	raw, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("digest must fail for a failed assertion:\n%s", raw)
	}
	text := string(raw)
	for _, want := range []string{"actual:", "src/problem.ts:9", "expected:", "[]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("digest output missing %q:\n%s", want, text)
		}
	}
}
