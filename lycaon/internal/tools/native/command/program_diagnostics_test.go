package command_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/observability/cmddiag"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProgramDiagnosticsQuietOnCleanDevRun(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "command-diagnostics.jsonl")
	t.Setenv("LYCAON_DEV_COMMAND_DIAGNOSTICS", "1")
	t.Setenv("LYCAON_DEV_COMMAND_DIAGNOSTICS_FILE", outFile)
	cmddiag.Close()
	t.Cleanup(cmddiag.Close)

	f := newProgramFixture(t)
	output, err := f.call(t, t.Context(), "printf 'clean-dev-run\\n'")
	testutil.FailErr(t, "run clean dev command", err)
	if !strings.Contains(output, "clean-dev-run") {
		t.Fatalf("expected command output preserved, got %s", output)
	}
	cmddiag.Close()

	data, err := os.ReadFile(outFile)
	if err != nil && !os.IsNotExist(err) {
		testutil.FailErr(t, "read diagnostics file", err)
	}
	if strings.TrimSpace(string(data)) != "" {
		t.Fatalf("expected 0 false-positive diagnostics on clean run, got:\n%s", string(data))
	}
}

func TestProgramDiagnosticsDisabledMatchesEnabledProductOutput(t *testing.T) {
	t.Setenv(debugpaths.FullDebugEnv, "")
	t.Setenv("LYCAON_DEV_COMMAND_DIAGNOSTICS", "")
	offFile := filepath.Join(t.TempDir(), "off.jsonl")
	t.Setenv("LYCAON_DEV_COMMAND_DIAGNOSTICS_FILE", offFile)
	cmddiag.Close()
	t.Cleanup(cmddiag.Close)

	fOff := newProgramFixture(t)
	outOff, err := fOff.call(t, t.Context(), "printf 'parity-check\\n'")
	testutil.FailErr(t, "run command with diagnostics off", err)
	if _, err := os.Stat(offFile); !os.IsNotExist(err) {
		t.Fatalf("diagnostics file created while disabled: err=%v", err)
	}

	onFile := filepath.Join(t.TempDir(), "on.jsonl")
	t.Setenv("LYCAON_DEV_COMMAND_DIAGNOSTICS", "1")
	t.Setenv("LYCAON_DEV_COMMAND_DIAGNOSTICS_FILE", onFile)
	cmddiag.Close()

	fOn := newProgramFixture(t)
	outOn, err := fOn.call(t, t.Context(), "printf 'parity-check\\n'")
	testutil.FailErr(t, "run command with diagnostics on", err)
	cmddiag.Close()

	if !strings.Contains(outOff, "parity-check") || !strings.Contains(outOn, "parity-check") {
		t.Fatalf("output mismatch off=%s on=%s", outOff, outOn)
	}
}

func TestProgramDiagnosticsSaturatedQueuePreservesProductExecution(t *testing.T) {
	gate := make(chan struct{})
	var closeGateOnce sync.Once
	closeGate := func() { closeGateOnce.Do(func() { close(gate) }) }
	firstEntered := make(chan struct{})
	var mu sync.Mutex
	var findings []cmddiag.Finding

	obs := cmddiag.NewObserver(cmddiag.Config{
		Enabled:       true,
		QueueCapacity: 1,
		Sink: func(f cmddiag.Finding) {
			if f.RuleID == cmddiag.RuleStaleTerminalPresentation {
				select {
				case <-firstEntered:
				default:
					close(firstEntered)
				}
				<-gate
			}
			mu.Lock()
			findings = append(findings, f)
			mu.Unlock()
		},
	})
	restore := cmddiag.SetObserver(obs)
	t.Cleanup(func() {
		restore()
		closeGate()
		obs.Close()
	})

	// Saturate the queue with a blocked synthetic finding before running real commands.
	stale := cmddiag.Observation{
		ProgramID:        "prog-block",
		ProgramState:     "completed",
		ProgramSettled:   true,
		ConsumerCaughtUp: true,
		Presentation:     &cmddiag.PresentationSnapshot{ProgramState: "running"},
	}
	obs.Submit(stale)
	<-firstEntered
	obs.Submit(stale)
	obs.Submit(stale)

	f := newProgramFixture(t)
	output, err := f.call(t, t.Context(), "printf 'intact-under-saturation\\n'")
	testutil.FailErr(t, "run command under saturated diagnostics queue", err)
	if !strings.Contains(output, "intact-under-saturation") {
		t.Fatalf("product execution altered under saturation: %s", output)
	}

	closeGate()
	obs.Close()
	if obs.Stats().Dropped == 0 {
		t.Fatal("expected saturated observer to record drops")
	}
	mu.Lock()
	defer mu.Unlock()
	sawOverflow := false
	for _, f := range findings {
		if f.RuleID == cmddiag.RuleCaptureOverflow && f.Disposition == cmddiag.DispositionOverflow {
			sawOverflow = true
		}
	}
	if !sawOverflow {
		t.Fatalf("expected capture_overflow finding under saturation, got %+v", findings)
	}
}
