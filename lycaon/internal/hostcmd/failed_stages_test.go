package hostcmd

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/exec"
)

func TestFailedStagesCarryTheExecutorsVerdict(t *testing.T) {
	sigpipe, failed := -1, 1
	stages := StageResultsFromRun([]exec.StageRun{
		{Command: "seq 1 400000", ExitCode: &sigpipe},
		{Command: "false", ExitCode: &failed, Failed: true},
		{Command: "echo skipped", Skipped: true},
	})
	if got := FailedStages(stages, 0); !slices.Equal(got, []string{"false"}) {
		t.Fatalf("failed stages = %q, want [false]", got)
	}
}

func TestFailedStagesWithoutStageStatusesUseTheInvocationStatus(t *testing.T) {
	stages := []StageResult{{Command: "vim AGENTS.md"}}
	if got := FailedStages(stages, 1); !slices.Equal(got, []string{"vim AGENTS.md"}) {
		t.Fatalf("failed capture = %q", got)
	}
	if got := FailedStages(stages, 0); len(got) != 0 {
		t.Fatalf("succeeded capture failed: %q", got)
	}
}
