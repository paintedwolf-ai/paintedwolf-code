package inject_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
)

func TestRenderCommandJobsBlockUsesSurfaceControls(t *testing.T) {
	now := time.Now().UTC()
	renderer := promptstest.InjectRenderer(t)
	jobs := []bgprocess.JobSnapshot{{
		Handle: "command-1", Mode: bgprocess.JobModeAwaited, OriginTool: "verify",
		StartedAt: now.Add(-time.Minute), Timeout: 10 * time.Minute,
		// A live job has not exited, so it carries no exit code.
		Stages: []hostcmd.StageResult{{Command: "./task check-fast"}},
	}}

	coordinator := inject.RenderCommandJobsBlock(
		context.Background(), renderer, "sess-inject-test", jobs, nil, now, "coordinator",
	)
	worker := inject.RenderCommandJobsBlock(
		context.Background(), renderer, "sess-inject-test", jobs, nil, now, "worker",
	)
	for name, block := range map[string]string{"coordinator": coordinator, "worker": worker} {
		if !strings.Contains(block, "handle=command-1") || !strings.Contains(block, "command_output") {
			t.Fatalf("%s ledger missing job controls: %q", name, block)
		}
	}
	for name, block := range map[string]string{"coordinator": coordinator, "worker": worker} {
		if !strings.Contains(block, `wait(until_complete=true,conditions=[{"kind":"process_done","handles":["<handle>"]}])`) {
			t.Fatalf("%s ledger missing exact wait control: %q", name, block)
		}
	}
}

func TestRenderCommandJobsBlockListsHeldCallsWithTheirControls(t *testing.T) {
	now := time.Now().UTC()
	renderer := promptstest.InjectRenderer(t)
	held := []heldcall.Running{{Handle: "held-1", Tool: "find", Elapsed: 14 * time.Minute}}

	block := inject.RenderCommandJobsBlock(context.Background(), renderer, "sess-inject-test", nil, held, now, "coordinator")
	for _, want := range []string{"handle=held-1", "mode=held", "tool=find", "held_result", "held_stop", `"kind":"process_done"`} {
		if !strings.Contains(block, want) {
			t.Fatalf("held ledger missing %q: %q", want, block)
		}
	}
	if strings.Contains(block, "command_output") || strings.Contains(block, "command=") {
		t.Fatalf("a held-only ledger names command controls: %q", block)
	}
}
