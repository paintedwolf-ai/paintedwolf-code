package exec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
)

func testOutputTarget(path string, appendMode bool) *OutputTarget {
	return &OutputTarget{Location: *testEffectLocation(path), Append: appendMode}
}

// runRedirected parses line, binds every redirection path under dir, and runs it there.
func runRedirected(t *testing.T, dir, line string) (*PipelineResult, error) {
	t.Helper()
	stages, err := StagesFromCommandLine(line)
	testutil.FailErr(t, "parse "+line, err)
	spec := &RedirectSpec{}
	for _, stage := range stages {
		for _, path := range stage.Streams.WritePaths() {
			spec.Bind(path, fseffect.Location{Root: dir, Rel: path})
		}
		if path := stage.Streams.Stdin.Path; path != "" {
			spec.Bind(path, fseffect.Location{Root: dir, Rel: path})
		}
	}
	return RunPipeline(context.Background(), stages, ExecOpts{
		Launch: HostLaunch("exec test"), Dir: dir, Timeout: 10 * time.Second,
		MaxOutputBytes: 4096, Redirect: spec,
	})
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	testutil.FailErr(t, "read "+filepath.Base(path), err)
	return string(body)
}

func TestStageRedirectionsStayOnTheirStage(t *testing.T) {
	dir := t.TempDir()
	res, err := runRedirected(t, dir, "echo first > one.txt && echo second > two.txt && echo shown")
	testutil.FailErr(t, "run sequence", err)
	if got := readFile(t, filepath.Join(dir, "one.txt")); got != "first\n" {
		t.Fatalf("one.txt = %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "two.txt")); got != "second\n" {
		t.Fatalf("two.txt = %q", got)
	}
	if string(res.Output) != "shown\n" {
		t.Fatalf("redirected output reached the capture: %q", res.Output)
	}
}

func TestStageOutputLandsBeforeTheNextGroupRuns(t *testing.T) {
	dir := t.TempDir()
	res, err := runRedirected(t, dir, "echo written > out.log && cat out.log")
	testutil.FailErr(t, "run write then read", err)
	if string(res.Output) != "written\n" {
		t.Fatalf("the next group read %q, want the committed file", res.Output)
	}
}

func TestStageRedirectionsKeepSeparateAppendModes(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "seed out", os.WriteFile(filepath.Join(dir, "out"), []byte("old out\n"), 0o644))
	testutil.FailErr(t, "seed err", os.WriteFile(filepath.Join(dir, "err"), []byte("old err\n"), 0o644))
	_, err := runRedirected(t, dir, `sh -c "echo new out; echo new err >&2" > out 2>> err`)
	testutil.FailErr(t, "run split modes", err)
	if got := readFile(t, filepath.Join(dir, "out")); got != "new out\n" {
		t.Fatalf("out = %q, want truncated", got)
	}
	if got := readFile(t, filepath.Join(dir, "err")); got != "old err\nnew err\n" {
		t.Fatalf("err = %q, want appended", got)
	}
}

func TestStageRedirectionsApplyInShellOrder(t *testing.T) {
	dir := t.TempDir()
	res, err := runRedirected(t, dir, `sh -c "echo out; echo err >&2" 2>&1 > /dev/null`)
	testutil.FailErr(t, "run dup then discard", err)
	if string(res.Output) != "err\n" {
		t.Fatalf("output = %q, want stderr only", res.Output)
	}

	res, err = runRedirected(t, dir, `sh -c "echo out; echo err >&2" 2>&1 > both.txt | cat`)
	testutil.FailErr(t, "run dup into pipe", err)
	if string(res.Output) != "err\n" {
		t.Fatalf("pipe received %q, want stderr only", res.Output)
	}
	if got := readFile(t, filepath.Join(dir, "both.txt")); got != "out\n" {
		t.Fatalf("both.txt = %q, want stdout only", got)
	}
}

func TestStageStdinRedirectionFeedsItsStage(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "seed input", os.WriteFile(filepath.Join(dir, "in.txt"), []byte("b\na\n"), 0o644))
	res, err := runRedirected(t, dir, "true && sort < in.txt")
	testutil.FailErr(t, "run stdin redirect", err)
	if string(res.Output) != "a\nb\n" {
		t.Fatalf("output = %q", res.Output)
	}
}

func TestStageRedirectionsThePipeCannotHonourAreRefused(t *testing.T) {
	for line, issue := range map[string]argv.RedirectionIssue{
		"echo x > f | cat":        argv.IssuePipeStdout,
		"echo x > /dev/null | wc": argv.IssuePipeStdout,
		"echo x | sort < f":       argv.IssuePipeStdin,
	} {
		stages, err := StagesFromCommandLine(line)
		testutil.FailErr(t, "parse "+line, err)
		err = ValidateStreams(stages)
		var redirection *argv.RedirectionError
		if !errors.As(err, &redirection) || redirection.Issue != issue {
			t.Fatalf("%q: err = %v, want %s", line, err, issue)
		}
	}
	for _, line := range []string{"echo x 2> f | cat", "echo x 2>&1 | cat", "echo x |& cat", "sort < f | cat"} {
		stages, err := StagesFromCommandLine(line)
		testutil.FailErr(t, "parse "+line, err)
		testutil.FailErr(t, "validate "+line, ValidateStreams(stages))
	}
}

func TestPipelineArrayKeepsStageRedirections(t *testing.T) {
	stages, err := StagesFromCommandLines([]string{"sort < in.txt", "uniq > out.txt 2>> err.txt"})
	testutil.FailErr(t, "parse pipeline array", err)
	if stages[0].Streams.Stdin.Path != "in.txt" {
		t.Fatalf("stdin path dropped: %+v", stages[0].Streams)
	}
	out, errSink := stages[1].Streams.StdoutSink(), stages[1].Streams.StderrSink()
	if out.Path != "out.txt" || out.Append || errSink.Path != "err.txt" || !errSink.Append {
		t.Fatalf("output redirections dropped: %+v", stages[1].Streams)
	}
}

func TestUnboundStageRedirectionFailsBeforeLaunch(t *testing.T) {
	stages, err := StagesFromCommandLine("echo x > out.txt")
	testutil.FailErr(t, "parse", err)
	_, err = RunPipeline(context.Background(), stages, ExecOpts{Launch: HostLaunch("exec test"), Dir: t.TempDir(), Timeout: 5 * time.Second})
	if !errors.Is(err, ErrRedirectUnbound) {
		t.Fatalf("err = %v, want ErrRedirectUnbound", err)
	}
}

func TestInternalArgvIsNeverGlobExpanded(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(dir, "a.go"), nil, 0o644))
	res, err := RunPipeline(context.Background(), []Stage{{Name: "echo", Args: []string{"*.go"}}}, ExecOpts{
		Launch: HostLaunch("exec test"), Dir: dir, Timeout: 5 * time.Second, MaxOutputBytes: 256,
	})
	testutil.FailErr(t, "run literal pattern", err)
	if strings.TrimSpace(string(res.Output)) != "*.go" {
		t.Fatalf("executor expanded an argument: %q", res.Output)
	}
}
