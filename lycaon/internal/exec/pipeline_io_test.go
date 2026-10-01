package exec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
)

func testEffectLocation(path string) *fseffect.Location {
	return &fseffect.Location{Root: filepath.Dir(path), Rel: filepath.Base(path)}
}

func TestRunPipelineStdinLiteral(t *testing.T) {
	dir := t.TempDir()
	res, err := RunPipeline(context.Background(), []Stage{
		{Name: "grep", Args: []string{"needle"}},
	}, ExecOpts{Launch: HostLaunch("exec test"),
		Dir:            dir,
		Timeout:        10 * time.Second,
		MaxOutputBytes: 4096,
		Stdin:          &StdinSpec{Literal: []byte("hay needle hay\n")},
	})
	testutil.FailErr(t, "RunPipeline grep stdin", err)
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	if !strings.Contains(string(res.Output), "needle") {
		t.Fatalf("output = %q", res.Output)
	}
}

func TestRunPipelineStdinFromFile(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "in.txt")
	testutil.FailErr(t, "WriteFile", os.WriteFile(inPath, []byte("alpha\nbeta\n"), 0o644))

	res, err := RunPipeline(context.Background(), []Stage{
		{Name: "grep", Args: []string{"beta"}},
	}, ExecOpts{Launch: HostLaunch("exec test"),
		Dir:            dir,
		Timeout:        10 * time.Second,
		MaxOutputBytes: 4096,
		Stdin:          &StdinSpec{From: testEffectLocation(inPath)},
	})
	testutil.FailErr(t, "RunPipeline stdin file", err)
	if !strings.Contains(string(res.Output), "beta") {
		t.Fatalf("output = %q", res.Output)
	}
}

func TestRunPipelineInlineEnv(t *testing.T) {
	dir := t.TempDir()
	res, err := RunPipeline(context.Background(), []Stage{
		{Name: "printenv", Args: []string{"PW_TEST_IO_PROBE"}},
	}, ExecOpts{Launch: HostLaunch("exec test"),
		Dir:            dir,
		Timeout:        10 * time.Second,
		MaxOutputBytes: 256,
		InlineEnv:      map[string]string{"PW_TEST_IO_PROBE": "seen"},
	})
	testutil.FailErr(t, "RunPipeline env", err)
	if !strings.Contains(string(res.Output), "seen") {
		t.Fatalf("output = %q", res.Output)
	}
}

func TestRunPipelineRedirectStdoutTruncateAndAppend(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "out.log")

	res, err := RunPipeline(context.Background(), []Stage{
		{Name: "echo", Args: []string{"first"}},
	}, ExecOpts{Launch: HostLaunch("exec test"),
		Dir:            dir,
		Timeout:        10 * time.Second,
		MaxOutputBytes: 4096,
		Redirect:       &RedirectSpec{Stdout: testOutputTarget(outPath, false)},
	})
	testutil.FailErr(t, "RunPipeline redirect truncate", err)
	if !strings.Contains(string(res.Output), "first") {
		t.Fatalf("capture tail missing output: %q", res.Output)
	}
	body, err := os.ReadFile(outPath)
	testutil.FailErr(t, "ReadFile out.log", err)
	if string(body) != "first\n" {
		t.Fatalf("file = %q", body)
	}

	_, err = RunPipeline(context.Background(), []Stage{
		{Name: "echo", Args: []string{"second"}},
	}, ExecOpts{Launch: HostLaunch("exec test"),
		Dir:            dir,
		Timeout:        10 * time.Second,
		MaxOutputBytes: 4096,
		Redirect:       &RedirectSpec{Stdout: testOutputTarget(outPath, true)},
	})
	testutil.FailErr(t, "RunPipeline redirect append", err)
	body, err = os.ReadFile(outPath)
	testutil.FailErr(t, "ReadFile appended", err)
	if !strings.Contains(string(body), "first") || !strings.Contains(string(body), "second") {
		t.Fatalf("appended file = %q", body)
	}
}

func TestRunPipelineRedirectWithPipelineLastStage(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "pipe.out")

	res, err := RunPipeline(context.Background(), []Stage{
		{Name: "echo", Args: []string{"piped"}},
		{Name: "cat", Args: nil},
	}, ExecOpts{Launch: HostLaunch("exec test"),
		Dir:            dir,
		Timeout:        10 * time.Second,
		MaxOutputBytes: 4096,
		Stdin:          &StdinSpec{Literal: []byte("ignored\n")},
		Redirect:       &RedirectSpec{Stdout: testOutputTarget(outPath, false)},
	})
	testutil.FailErr(t, "RunPipeline pipeline redirect", err)
	if !strings.Contains(string(res.Output), "piped") {
		t.Fatalf("tail = %q", res.Output)
	}
	body, err := os.ReadFile(outPath)
	testutil.FailErr(t, "ReadFile pipe.out", err)
	if !strings.Contains(string(body), "piped") {
		t.Fatalf("redirect file = %q", body)
	}
}

func TestRunPipelineStdinLiteralTooLarge(t *testing.T) {
	_, err := RunPipeline(context.Background(), []Stage{
		{Name: "cat", Args: nil},
	}, ExecOpts{Launch: HostLaunch("exec test"),
		Timeout: 5 * time.Second,
		Stdin:   &StdinSpec{Literal: []byte(strings.Repeat("x", DefaultMaxStdinBytes+1))},
	})
	if !errors.Is(err, ErrStdinTooLarge) {
		t.Fatalf("err = %v, want ErrStdinTooLarge", err)
	}
}

func TestRunPipelineInlineEnvRejectsGIT(t *testing.T) {
	_, err := RunPipeline(context.Background(), []Stage{
		{Name: "true", Args: nil},
	}, ExecOpts{Launch: HostLaunch("exec test"),
		Timeout:   5 * time.Second,
		InlineEnv: map[string]string{"GIT_DIR": "/tmp"},
	})
	if !errors.Is(err, ErrBlockedEnvKey) {
		t.Fatalf("err = %v, want ErrBlockedEnvKey", err)
	}
}
