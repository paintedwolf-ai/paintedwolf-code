package exec

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRunPipelineSingleStageParityWithRun(t *testing.T) {
	dir := t.TempDir()
	opts := ExecOpts{Launch: HostLaunch("exec test"), Dir: dir, Timeout: 30 * time.Second}

	runOut, runCode, runErr := Run(context.Background(), "go", []string{"version"}, opts)
	testutil.FailErr(t, "Run go version", runErr)
	if runCode != 0 {
		t.Fatalf("Run exit = %d", runCode)
	}

	pipeRes, pipeErr := RunPipeline(context.Background(), []Stage{{Name: "go", Args: []string{"version"}}}, opts)
	testutil.FailErr(t, "RunPipeline go version", pipeErr)
	if pipeRes.ExitCode != 0 {
		t.Fatalf("RunPipeline exit = %d", pipeRes.ExitCode)
	}
	if len(pipeRes.Stages) != 1 {
		t.Fatalf("stages = %d, want 1", len(pipeRes.Stages))
	}
	if pipeRes.Stages[0].ExitStatus() != 0 {
		t.Fatalf("stage exit = %d", pipeRes.Stages[0].ExitStatus())
	}
	if string(pipeRes.Output) != string(runOut) {
		t.Fatalf("output mismatch:\nRun: %q\nPipeline: %q", runOut, pipeRes.Output)
	}
}

func TestRunPipelineMultiStageSuccess(t *testing.T) {
	dir := t.TempDir()
	res, err := RunPipeline(context.Background(), []Stage{
		{Name: "echo", Args: []string{"alpha"}},
		{Name: "cat", Args: nil},
		{Name: "wc", Args: []string{"-c"}},
	}, ExecOpts{Launch: HostLaunch("exec test"), Dir: dir, Timeout: 10 * time.Second})
	testutil.FailErr(t, "RunPipeline echo|cat|wc", err)
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	for i, stage := range res.Stages {
		if stage.ExitStatus() != 0 {
			t.Fatalf("stage %d exit = %d", i, stage.ExitStatus())
		}
	}
	if !strings.Contains(string(res.Output), "6") {
		t.Fatalf("output = %q, want byte count", res.Output)
	}
}

func TestRunPipelineMidStageFailure(t *testing.T) {
	dir := t.TempDir()
	res, err := RunPipeline(context.Background(), []Stage{
		{Name: "false", Args: nil},
		{Name: "echo", Args: []string{"never"}},
	}, ExecOpts{Launch: HostLaunch("exec test"), Dir: dir, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil {
		t.Fatal("expected pipeline result")
	}
	if res.ExitCode == 0 {
		t.Fatal("expected first stage non-zero exit")
	}
	if res.Stages[0].ExitStatus() == 0 {
		t.Fatal("expected stage 0 failure")
	}
}

func TestRunPipelineWholePipelineTimeout(t *testing.T) {
	dir := t.TempDir()
	res, err := RunPipeline(context.Background(), []Stage{
		{Name: "sleep", Args: []string{"5"}},
		{Name: "echo", Args: []string{"late"}},
	}, ExecOpts{Launch: HostLaunch("exec test"), Dir: dir, Timeout: 200 * time.Millisecond})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if res == nil || !res.TimedOut {
		t.Fatalf("expected timed out result, got res=%v err=%v", res, err)
	}
}

func TestRunPipelineCaptureCap(t *testing.T) {
	dir := t.TempDir()
	maxOut := 256
	res, err := RunPipeline(context.Background(), []Stage{
		{Name: "python3", Args: []string{"-c", "import sys; sys.stdout.write('x' * 10000)"}},
		{Name: "cat", Args: nil},
	}, ExecOpts{Launch: HostLaunch("exec test"), Dir: dir, Timeout: 10 * time.Second, MaxOutputBytes: maxOut})
	if !errors.Is(err, ErrOutputTruncated) {
		t.Fatalf("expected ErrOutputTruncated, got %v", err)
	}
	if res == nil {
		t.Fatal("expected partial result")
	}
	if len(res.Output) > maxOut {
		t.Fatalf("output len = %d, cap = %d", len(res.Output), maxOut)
	}
}

func TestRunPipelineRejectsEmptyStages(t *testing.T) {
	_, err := RunPipeline(context.Background(), nil, ExecOpts{Launch: HostLaunch("exec test")})
	if err == nil {
		t.Fatal("expected error for empty pipeline")
	}
}

func TestOverlayProcessEnvHostValuesReplaceAmbientDuplicates(t *testing.T) {
	base := []string{
		"PATH=/bin",
		"HTTP_PROXY=http://ambient.invalid",
		"http_proxy=http://ambient-lower.invalid",
		"NO_PROXY=*",
	}
	overrides := []string{
		"HTTP_PROXY=http://host-managed.invalid",
		"http_proxy=http://host-managed.invalid",
		"NO_PROXY=localhost",
	}
	got := overlayProcessEnv(base, overrides)
	values := envSliceToMap(got)
	if values["HTTP_PROXY"] != "http://host-managed.invalid" ||
		values["http_proxy"] != "http://host-managed.invalid" || values["NO_PROXY"] != "localhost" {
		t.Fatalf("host-managed proxy environment did not win: %v", got)
	}
	for _, key := range []string{"HTTP_PROXY", "http_proxy", "NO_PROXY"} {
		count := 0
		for _, entry := range got {
			if strings.HasPrefix(entry, key+"=") {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("%s appears %d times in %v", key, count, got)
		}
	}
}
