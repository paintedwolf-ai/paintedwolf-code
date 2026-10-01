package exec

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/testutil"
)

func sequenceStages(t *testing.T, line string) []Stage {
	t.Helper()
	stages, err := StagesFromCommandLine(line)
	testutil.FailErr(t, "parse command line", err)
	return stages
}

func runSequence(t *testing.T, line string) *PipelineResult {
	t.Helper()
	res, err := RunPipeline(context.Background(), sequenceStages(t, line),
		ExecOpts{Launch: HostLaunch("exec test")})
	if res == nil {
		testutil.FailErr(t, "run sequence", err)
	}
	return res
}

// `&&` gates on the previous group. A failing left side leaves the right side
// unrun, and an unrun stage has no exit status to report.
func TestSequenceAndShortCircuits(t *testing.T) {
	res := runSequence(t, "false && echo ran")
	if res.ExitCode == 0 {
		t.Fatalf("exit = %d, want non-zero", res.ExitCode)
	}
	if len(res.Stages) != 2 {
		t.Fatalf("stages = %d, want 2", len(res.Stages))
	}
	if res.Stages[0].Skipped {
		t.Fatal("first stage must run")
	}
	if !res.Stages[1].Skipped {
		t.Fatal("second stage must be skipped after a failing &&")
	}
	if res.Stages[1].ExitCode != nil {
		t.Fatalf("skipped stage exit = %d, want none", *res.Stages[1].ExitCode)
	}
	if res.Stages[1].Connector != argv.ConnectorAnd {
		t.Fatalf("connector = %q, want %q", res.Stages[1].Connector, argv.ConnectorAnd)
	}
	if strings.Contains(string(res.Output), "ran") {
		t.Fatalf("skipped stage produced output: %q", res.Output)
	}
}

func TestSequenceAndRunsAfterSuccess(t *testing.T) {
	res := runSequence(t, "true && echo ran")
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, want 0", res.ExitCode)
	}
	for i, stage := range res.Stages {
		if stage.Skipped {
			t.Fatalf("stage %d skipped, want run", i)
		}
	}
	if !strings.Contains(string(res.Output), "ran") {
		t.Fatalf("output = %q, want the second stage's output", res.Output)
	}
}

// `a && b && c` must skip c as well: a skipped group does not change the status the
// next connector reads.
func TestSequenceSkipCarriesThroughChain(t *testing.T) {
	res := runSequence(t, "false && echo one && echo two")
	if len(res.Stages) != 3 {
		t.Fatalf("stages = %d, want 3", len(res.Stages))
	}
	if !res.Stages[1].Skipped || !res.Stages[2].Skipped {
		t.Fatalf("stages 1 and 2 must both be skipped: %+v", res.Stages)
	}
	if res.ExitCode == 0 {
		t.Fatalf("exit = %d, want the failing first group's status", res.ExitCode)
	}
}

func TestSequenceOrSemantics(t *testing.T) {
	// A successful left side leaves `||` unrun, and the plan exits 0.
	res := runSequence(t, "true || echo fallback")
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, want 0", res.ExitCode)
	}
	if !res.Stages[1].Skipped {
		t.Fatal("second stage must be skipped after a succeeding ||")
	}

	// A failing left side runs the right one, and its success is the plan's status.
	res = runSequence(t, "false || echo fallback")
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, want 0 from the recovered group", res.ExitCode)
	}
	if res.Stages[1].Skipped {
		t.Fatal("second stage must run after a failing ||")
	}
	if !strings.Contains(string(res.Output), "fallback") {
		t.Fatalf("output = %q, want the fallback output", res.Output)
	}
	// The recovered plan still records which stage failed.
	if !res.Stages[0].Failed || res.Stages[1].Failed {
		t.Fatalf("failed verdicts = %v, %v; want the left side failed", res.Stages[0].Failed, res.Stages[1].Failed)
	}
}

// `;` always continues, and the plan's status is the last group that ran — not the
// first failure, which is how a pipeline reports.
func TestSequenceSemicolonAlwaysRunsAndTakesLastStatus(t *testing.T) {
	res := runSequence(t, "false; true")
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, want 0 from the last group", res.ExitCode)
	}
	for i, stage := range res.Stages {
		if stage.Skipped {
			t.Fatalf("stage %d skipped, want run", i)
		}
	}

	res = runSequence(t, "true; false")
	if res.ExitCode == 0 {
		t.Fatal("exit = 0, want the last group's failure")
	}
}

// Groups run in order and share the capture, so output arrives in the order the
// line states.
func TestSequenceOutputOrder(t *testing.T) {
	res := runSequence(t, "echo first && echo second && echo third")
	out := string(res.Output)
	first, second, third := strings.Index(out, "first"), strings.Index(out, "second"), strings.Index(out, "third")
	if first < 0 || second < 0 || third < 0 {
		t.Fatalf("output = %q, want all three groups", out)
	}
	if !(first < second && second < third) {
		t.Fatalf("output out of order: %q", out)
	}
}

// A plan with no sequencing keeps its existing shape exactly: one group, piped.
func TestGroupStagesPartitions(t *testing.T) {
	pipeStages, err := StagesFromCommandLines([]string{"echo hi", "cat", "wc -l"})
	testutil.FailErr(t, "parse pipeline", err)
	if groups := GroupStages(pipeStages); len(groups) != 1 || groups[0].Len() != 3 {
		t.Fatalf("pipeline groups = %+v, want one group of 3", groups)
	}

	seq := sequenceStages(t, "a && b; c")
	groups := GroupStages(seq)
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3", len(groups))
	}
	want := []argv.Connector{argv.ConnectorNone, argv.ConnectorAnd, argv.ConnectorSeq}
	for i, group := range groups {
		if group.Connector != want[i] {
			t.Fatalf("group %d connector = %q, want %q", i, group.Connector, want[i])
		}
	}
}

// A sequenced pipeline stage would have no stdout to wire forward, so the pipeline
// array keeps one argv vector per element.
func TestPipelineStageRejectsSequencing(t *testing.T) {
	if _, err := StagesFromCommandLines([]string{"go build && go test"}); err == nil {
		t.Fatal("expected a sequenced pipeline stage to be rejected")
	}
}

func TestInlinePipesAndRedirections(t *testing.T) {
	t.Run("inline pipe", func(t *testing.T) {
		res := runSequence(t, "echo alpha | wc -c")
		if res.ExitCode != 0 {
			t.Fatalf("exit = %d", res.ExitCode)
		}
		if !strings.Contains(string(res.Output), "6") {
			t.Fatalf("output = %q, want 6", res.Output)
		}
	})

	t.Run("dev null discard", func(t *testing.T) {
		res := runSequence(t, "echo alpha > /dev/null")
		if res.ExitCode != 0 {
			t.Fatalf("exit = %d", res.ExitCode)
		}
		if len(strings.TrimSpace(string(res.Output))) != 0 {
			t.Fatalf("output = %q, want empty", res.Output)
		}
	})

	t.Run("stderr merge into stdout", func(t *testing.T) {
		res := runSequence(t, "sh -c 'echo err >&2' 2>&1")
		if res.ExitCode != 0 {
			t.Fatalf("exit = %d", res.ExitCode)
		}
		if !strings.Contains(string(res.Output), "err") {
			t.Fatalf("output = %q, want err", res.Output)
		}
	})

	t.Run("pipe merged operator", func(t *testing.T) {
		res := runSequence(t, "sh -c 'echo err >&2' |& cat")
		if res.ExitCode != 0 {
			t.Fatalf("exit = %d", res.ExitCode)
		}
		if !strings.Contains(string(res.Output), "err") {
			t.Fatalf("output = %q, want err", res.Output)
		}
	})
}

// A later group that cannot start ends the run with its launch error, not a
// bare exit status.
func TestSequenceLaterLaunchFailureKeepsItsError(t *testing.T) {
	res, err := RunPipeline(context.Background(), sequenceStages(t, "true && ./no-such-binary-for-launch"),
		ExecOpts{Launch: HostLaunch("exec test"), Dir: t.TempDir()})
	if res == nil || res.ExitCode != -1 {
		t.Fatalf("result = %+v, want exit -1", res)
	}
	if !errors.Is(err, ErrStageNotLaunched) || !strings.Contains(err.Error(), "no-such-binary-for-launch") {
		t.Fatalf("err = %v, want the stage's launch error", err)
	}
}
