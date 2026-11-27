package exec

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Scanner CLIs print progress to stderr and the report to stdout.
func TestRunSeparateKeepsStdoutParseable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh for a two-stream fixture")
	}
	const payload = `{"version":"2.1.0","runs":[]}`
	stdout, stderr, exitCode, err := RunSeparate(
		context.Background(),
		"sh",
		[]string{"-c", "echo 'INFO scanning...' 1>&2; printf '%s' '" + payload + "'; exit 1"},
		ExecOpts{Launch: HostLaunch("exec test")},
	)
	if err != nil {
		testutil.FailErr(t, "run separate", err)
	}
	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1 (scanners exit non-zero on findings)", exitCode)
	}
	var doc map[string]any
	if err := json.Unmarshal(stdout, &doc); err != nil {
		testutil.FailErr(t, "stdout must be valid JSON with stderr separated", err)
	}
	if !strings.Contains(string(stderr), "INFO scanning") {
		t.Fatalf("stderr = %q, want the progress line", string(stderr))
	}
}

// Run concatenates stdout and stderr.
func TestRunMergesStderrByDefault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh for a two-stream fixture")
	}
	out, _, err := Run(
		context.Background(),
		"sh",
		[]string{"-c", "echo out; echo err 1>&2"},
		ExecOpts{Launch: HostLaunch("exec test")},
	)
	if err != nil {
		testutil.FailErr(t, "run", err)
	}
	merged := string(out)
	if !strings.Contains(merged, "out") || !strings.Contains(merged, "err") {
		t.Fatalf("merged output = %q, want both streams", merged)
	}
}

func TestScanOutputCapExceedsDefault(t *testing.T) {
	if DefaultMaxScanOutputBytes <= DefaultMaxOutputBytes {
		t.Fatalf("scan cap %d must exceed the log cap %d — SARIF reports are documents, not logs",
			DefaultMaxScanOutputBytes, DefaultMaxOutputBytes)
	}
}

func TestPipelineKeepsDiagnosticsOnPartialLaunchCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell pipeline fixture")
	}
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	res, err := RunPipeline(ctx, []Stage{
		{Name: "sh", Args: []string{"-c", "echo launch-diagnostic >&2 && touch emitted"}},
		{Name: "cat"},
	}, ExecOpts{
		Launch: HostLaunch("exec test"), Dir: dir, SeparateStderr: true,
		Started: func(int) {
			testutil.WaitFor(t, "first stage emitted diagnostics", func() bool {
				_, err := os.Stat(filepath.Join(dir, "emitted"))
				return err == nil
			})
			cancel()
		},
	})
	if !errors.Is(err, context.Canceled) || res == nil || res.ExitCode != -1 || strings.TrimSpace(string(res.Stderr)) != "launch-diagnostic" {
		t.Fatalf("partial launch returned result %+v, error %v", res, err)
	}
}
