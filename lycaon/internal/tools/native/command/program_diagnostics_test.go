package command_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/observability/cmddiag"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProgramDiagnosticsQuietOnCleanDevRun(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "command-diagnostics.jsonl")
	t.Setenv("LYCAON_DEV_COMMAND_DIAGNOSTICS", "1")
	t.Setenv("LYCAON_DEV_COMMAND_DIAGNOSTICS_FILE", outFile)
	cmddiag.Close()
	t.Cleanup(cmddiag.Close)

	f := newProgramFixture(t)
	f.Configure = func(tc *tools.ToolContext) {
		tc.Effects.ReportProgress = func(api.ToolProgress) {}
	}
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
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var got cmddiag.Finding
		testutil.FailErr(t, "decode finding", json.Unmarshal([]byte(line), &got))
		if got.Disposition == cmddiag.DispositionContradiction {
			t.Fatalf("unexpected contradiction on clean dev run: %+v", got)
		}
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

func TestProgramDiagnosticsCanaryLeaksNoPathContentOrArgument(t *testing.T) {
	const (
		canaryDir  = "canary_dir_9876"
		canaryFile = "canary_file_5432.txt"
		canaryBody = "CANARY_FILE_BODY_ALPHA_7788"
		canaryArg  = "CANARY_CMD_ARG_OMEGA_9900"
	)
	outFile := filepath.Join(t.TempDir(), "command-diagnostics.jsonl")
	t.Setenv("LYCAON_DEV_COMMAND_DIAGNOSTICS", "1")
	t.Setenv("LYCAON_DEV_COMMAND_DIAGNOSTICS_FILE", outFile)
	cmddiag.Close()
	t.Cleanup(cmddiag.Close)

	f := newProgramFixture(t)
	// Leave ReportProgress nil so the unsupported presentation finding is written
	// to command-diagnostics.jsonl, exercising real JSONL serialization.
	target := filepath.Join(f.Dir, canaryDir, canaryFile)
	testutil.FailErr(t, "mkdir canary dir", os.MkdirAll(filepath.Dir(target), 0o755))
	testutil.FailErr(t, "write canary file", os.WriteFile(target, []byte(canaryBody+"\n"), 0o600))

	cmd := "printf '%s\\n' '" + canaryArg + "' | head"
	output, err := f.call(t, t.Context(), cmd)
	testutil.FailErr(t, "run canary command", err)
	if !strings.Contains(output, canaryArg) {
		t.Fatalf("expected command output preserved, got %s", output)
	}
	cmddiag.Close()

	raw, err := os.ReadFile(outFile)
	testutil.FailErr(t, "read diagnostics jsonl", err)
	body := string(raw)
	if strings.TrimSpace(body) == "" {
		t.Fatal("expected at least one metadata finding written for canary verification")
	}
	for _, sentinel := range []string{f.Dir, canaryDir, canaryFile, canaryBody, canaryArg} {
		if strings.Contains(body, sentinel) {
			t.Fatalf("command-diagnostics.jsonl leaked sentinel %q:\n%s", sentinel, body)
		}
	}
}
