package bundleddriver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/scan/opengrep"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testutil"
)

const budgetScannerScript = `#!/bin/sh
output=""; rule_timeout="${SEMGREP_TIMEOUT:-5}"
while [ "$#" -gt 0 ]; do
 case "$1" in
  --output) shift; output="$1" ;;
  --timeout) shift; rule_timeout="$1" ;;
 esac
 shift
done
if [ "${SCANNER_SKIP_REPORT:-}" = 1 ]; then exit 0; fi
if [ "$rule_timeout" != 0 ]; then
 printf '%s' '{"results":[],"errors":[{"type":"Timeout","level":"error","message":"Per-rule timer interrupted coverage"}],"paths":{"scanned":[]}}' > "$output"
 exit 0
fi
printf '%s' '{"results":[],"errors":[],"paths":{"scanned":[]}}' > "$output"
if [ "${SCANNER_HOLD_AFTER_REPORT:-}" = 1 ]; then exec /bin/sleep 300; fi
`

func newBudgetScanner(t *testing.T) (*OpenGrepScanner, string, opengrepRunFiles) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell scanner fixture requires macOS or Linux")
	}
	root := t.TempDir()
	testutil.FailErr(t, "write budget scanner fixture", os.WriteFile(filepath.Join(root, "scanner"), []byte(budgetScannerScript), 0o700))
	files, cleanup, err := newOpengrepRunFiles()
	testutil.FailErr(t, "create scanner output", err)
	t.Cleanup(cleanup)
	scanner := NewOpenGrepScanner(OpenGrepOptions{Jobs: 1, RuntimePolicy: scancatalog.RuntimePolicy{SoftLimitSec: 900, HardLimitSec: 7200, CPUUnits: 1, Parallelism: 1}})
	return scanner, root, files
}

func TestOpenGrepProductionInvocationOverridesAmbientRuleTimeout(t *testing.T) {
	t.Setenv("SEMGREP_TIMEOUT", "1")
	t.Setenv("SCANNER_HOLD_AFTER_REPORT", "")
	scanner, root, files := newBudgetScanner(t)
	result, err := scanner.scanTargets(t.Context(), root, filepath.Join(root, "scanner"), []string{"fixture-rule"}, []string{root}, nil, opengrep.Analysis{Mode: opengrep.Intraprocedural}, files)
	testutil.FailErr(t, "scan with host-owned budget", err)
	if result == nil || result.FindingsCount != 0 || len(result.Warnings) != 0 {
		t.Fatalf("incomplete scan admitted: %#v", result)
	}
}

func TestOpenGrepCancellationRejectsUnfinishedReport(t *testing.T) {
	t.Setenv("SCANNER_HOLD_AFTER_REPORT", "1")
	scanner, root, files := newBudgetScanner(t)
	ctx, cancel := context.WithCancel(t.Context())
	var result *scanoutput.Result
	var err error
	done := make(chan struct{})
	go func() {
		defer close(done)
		result, err = scanner.scanTargets(ctx, root, filepath.Join(root, "scanner"), []string{"fixture-rule"}, []string{root}, nil, opengrep.Analysis{Mode: opengrep.Intraprocedural}, files)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(testutil.Timeout(15 * time.Second)):
			t.Error("scanner process did not stop after cancellation")
		}
	})
	testutil.WaitFor(t, 15*time.Second, func() bool {
		select {
		case <-done:
			t.Fatalf("scanner exited before report readiness: %v", err)
		default:
		}
		report, readErr := os.ReadFile(files.jsonPath)
		return readErr == nil && string(report) == `{"results":[],"errors":[],"paths":{"scanned":[]}}`
	})
	if ctx.Err() != nil {
		t.Fatalf("scan context canceled before report readiness: %v", ctx.Err())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(testutil.Timeout(5 * time.Second)):
		t.Fatal("scanner did not return after cancellation")
	}
	if result != nil || !errors.Is(err, context.Canceled) || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("canceled scanner admitted its unfinished report: result=%#v error=%v", result, err)
	}
}

func TestOpenGrepHostDeadlineRemainsAuthoritative(t *testing.T) {
	scanner, root, files := newBudgetScanner(t)
	ctx, cancel := context.WithDeadline(t.Context(), time.Unix(0, 0))
	defer cancel()
	result, err := scanner.scanTargets(ctx, root, filepath.Join(root, "scanner"), []string{"fixture-rule"}, []string{root}, nil, opengrep.Analysis{Mode: opengrep.Intraprocedural}, files)
	if result != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired host deadline: result=%#v error=%v", result, err)
	}
}

func TestOpenGrepBatchCannotReuseAnEarlierReport(t *testing.T) {
	t.Setenv("SCANNER_HOLD_AFTER_REPORT", "")
	t.Setenv("SCANNER_SKIP_REPORT", "")
	scanner, root, files := newBudgetScanner(t)
	run := func() (*scanoutput.Result, error) {
		return scanner.scanTargets(t.Context(), root, filepath.Join(root, "scanner"), []string{"fixture-rule"}, []string{root}, nil, opengrep.Analysis{Mode: opengrep.Intraprocedural}, files)
	}
	_, err := run()
	testutil.FailErr(t, "publish first batch report", err)
	t.Setenv("SCANNER_SKIP_REPORT", "1")
	result, err := run()
	if err == nil || result != nil {
		t.Fatalf("missing batch output reused a prior report: result=%+v error=%v", result, err)
	}
}
