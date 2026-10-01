//go:build unix

package exec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// A downstream stage that exits early must end the pipeline promptly (the
// producer gets SIGPIPE once the parent drops its pipe fds) and must not fail
// it: the producer's signal death is how `… | head` finishes.
func TestRunPipelineEarlyExitDownstreamEndsPromptlyAndSucceeds(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	res, err := RunPipeline(context.Background(), []Stage{
		{Name: "seq", Args: []string{"1", "400000"}},
		{Name: "head", Args: []string{"-n", "1"}},
	}, ExecOpts{Launch: HostLaunch("exec test"), Dir: dir, Timeout: 30 * time.Second, MaxOutputBytes: 4096})
	elapsed := time.Since(start)
	testutil.FailErr(t, "RunPipeline seq|head", err)
	if res.TimedOut {
		t.Fatalf("pipeline hung until timeout (%v)", elapsed)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("pipeline took %v; early-exit downstream should end it promptly", elapsed)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d (stages %+v); SIGPIPE on a non-final stage is not a failure", res.ExitCode, res.Stages)
	}
	for i, stage := range res.Stages {
		if stage.Failed {
			t.Fatalf("stage %d failed (%+v); SIGPIPE on a non-final stage is not a failure", i, stage)
		}
	}
	if strings.TrimSpace(string(res.Output)) != "1" {
		t.Fatalf("output = %q", res.Output)
	}
}

// A final stage that fails still fails the pipeline even when an upstream stage
// died of SIGPIPE.
func TestRunPipelineFinalStageFailureStillFails(t *testing.T) {
	dir := t.TempDir()
	res, err := RunPipeline(context.Background(), []Stage{
		{Name: "echo", Args: []string{"x"}},
		{Name: "false", Args: nil},
	}, ExecOpts{Launch: HostLaunch("exec test"), Dir: dir, Timeout: 10 * time.Second, MaxOutputBytes: 4096})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ExitCode == 0 {
		t.Fatalf("exit = 0, want failure from final stage; stages %+v", res.Stages)
	}
	if res.Stages[0].Failed || !res.Stages[1].Failed {
		t.Fatalf("failed verdicts = %v, %v; want only the final stage", res.Stages[0].Failed, res.Stages[1].Failed)
	}
}

// stdout_to and stderr_to naming the same file share one descriptor: both
// streams land in the file instead of overwriting each other from offset 0.
func TestRunPipelineRedirectSamePathKeepsBothStreams(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "both.log")
	res, err := RunPipeline(context.Background(), []Stage{
		{Name: "python3", Args: []string{"-c", "import sys; sys.stdout.write('OUTLINE\\n'); sys.stdout.flush(); sys.stderr.write('ERRLINE\\n')"}},
	}, ExecOpts{Launch: HostLaunch("exec test"),
		Dir: dir, Timeout: 10 * time.Second, MaxOutputBytes: 4096,
		Redirect: &RedirectSpec{Stdout: testOutputTarget(out, false), Stderr: testOutputTarget(out, false)},
	})
	testutil.FailErr(t, "RunPipeline same-path redirect", err)
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	body, rerr := os.ReadFile(out)
	testutil.FailErr(t, "ReadFile both.log", rerr)
	if !strings.Contains(string(body), "OUTLINE") || !strings.Contains(string(body), "ERRLINE") {
		t.Fatalf("file = %q, want both streams", body)
	}
}
