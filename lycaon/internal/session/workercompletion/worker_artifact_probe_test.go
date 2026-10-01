package workercompletion_test

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/testutil"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/pkg/api"
)

// stubWorkspaceChecker answers the workspace probe without shelling out to git.
type stubWorkspaceChecker struct {
	changed bool
	err     error
}

func (s stubWorkspaceChecker) HasChanges(context.Context, string) (bool, error) {
	return s.changed, s.err
}

// commandRanMessages is a worker that ran one successful command tool, the
// shape that falls back to the workspace probe.
func commandRanMessages() []api.Message {
	return []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "tc1", Name: "command", Args: map[string]any{"command": "go run ./cmd/codegen"}},
			},
		},
		{Role: api.MessageRoleTool, Content: "generated 1 file"},
	}
}

func artifactCheck(t *testing.T, grounding api.CitationGrounding) api.CitationGroundingCheck {
	t.Helper()
	for _, check := range grounding.Checks {
		if check.ID == "implementer_artifact" {
			return check
		}
	}
	t.Fatalf("no implementer_artifact check in %+v", grounding.Checks)
	return api.CitationGroundingCheck{}
}

// A probe that could not run says nothing about the worker's artifact, so it
// records advisory rather than rejecting.
func TestEvaluateWorkerSummaryUndeterminedWorkspaceProbeDoesNotClaimNoArtifact(t *testing.T) {
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType:      orchestration.ProfileImplementer,
		Report:         workercompletion.WorkerCompletionReport{Brief: "Ran code generator; new file landed on disk."},
		ChildMessages:  commandRanMessages(),
		ProjectDir:     t.TempDir(),
		WorkspaceCheck: stubWorkspaceChecker{err: errors.New("git status: subprocess failed")},
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)

	if eval.Status != "complete" {
		t.Fatalf("status = %q want complete: an undetermined probe must not reject (eval = %+v)", eval.Status, eval)
	}
	if eval.HintCode != "" {
		t.Fatalf("hint code = %q want empty", eval.HintCode)
	}
	check := artifactCheck(t, eval.Grounding)
	if check.Status != api.CitationGroundingCheckStatusAdvisory {
		t.Fatalf("check status = %q want advisory (neither pass nor failure)", check.Status)
	}
	if strings.Contains(check.Summary, "No mutation tool activity or workspace changes detected") {
		t.Fatalf("summary asserts an unestablished fact: %q", check.Summary)
	}
	if !strings.Contains(check.Summary, "could not run") {
		t.Fatalf("summary = %q want it to say the probe could not run", check.Summary)
	}
}

// A probe that ran and found a clean workspace still rejects.
func TestEvaluateWorkerSummaryDeterminedCleanWorkspaceStillRejects(t *testing.T) {
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType:      orchestration.ProfileImplementer,
		Report:         workercompletion.WorkerCompletionReport{Brief: "Ran code generator; new file landed on disk."},
		ChildMessages:  commandRanMessages(),
		ProjectDir:     t.TempDir(),
		WorkspaceCheck: stubWorkspaceChecker{changed: false},
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)

	if eval.Status != "partial" || eval.HintCode != "WORKER_SUMMARY_NO_ARTIFACT" {
		t.Fatalf("eval = %+v want partial/WORKER_SUMMARY_NO_ARTIFACT for a clean workspace", eval)
	}
	if check := artifactCheck(t, eval.Grounding); check.Status != api.CitationGroundingCheckStatusFailed {
		t.Fatalf("check status = %q want failed", check.Status)
	}
}

// A dirty workspace proves the artifact for a command-only worker.
func TestEvaluateWorkerSummaryDeterminedDirtyWorkspaceCompletes(t *testing.T) {
	eval, workerEvalErr := workercompletion.EvaluateWorkerSummary(context.Background(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
		AgentType:      orchestration.ProfileImplementer,
		Report:         workercompletion.WorkerCompletionReport{Brief: "Ran code generator; new file landed on disk."},
		ChildMessages:  commandRanMessages(),
		ProjectDir:     t.TempDir(),
		WorkspaceCheck: stubWorkspaceChecker{changed: true},
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)

	if eval.Status != "complete" {
		t.Fatalf("eval = %+v want complete for a dirty workspace", eval)
	}
	if check := artifactCheck(t, eval.Grounding); check.Status != api.CitationGroundingCheckStatusPassed {
		t.Fatalf("check status = %q want passed", check.Status)
	}
}

// Only a probe that ran and found nothing may fire WORKER_IMPLEMENT_NO_ARTIFACT.
func TestObserveImplementerFinishRejectsOnlyOnDeterminedAbsence(t *testing.T) {
	child := &api.Session{
		ID:              "child-1",
		ParentSessionID: "parent-1",
		AgentType:       orchestration.ProfileImplementer,
		WorkspacePath:   t.TempDir(),
	}
	// The rejected edit leaves no ledger proof; the later successful command
	// carries the run past the successful-tool guard onto the probe.
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "tc1", Name: "edit", Args: map[string]any{"path": "game.py"}},
			},
		},
		{
			Role:       api.MessageRoleTool,
			Content:    "Rejected: missing path",
			ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeRejected},
		},
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "tc2", Name: "command", Args: map[string]any{"command": "go build ./..."}},
			},
		},
		{Role: api.MessageRoleTool, Content: "ok"},
	}

	undetermined := oar.NewGuardContext()
	workercompletion.ObserveImplementerFinishWithoutWrite(
		context.Background(), child, msgs, "I attempted to edit game.py.",
		child.WorkspacePath, stubWorkspaceChecker{err: errors.New("git status: subprocess failed")}, undetermined,
	)
	if _, rejected := undetermined.RejectData["WORKER_IMPLEMENT_NO_ARTIFACT"]; rejected {
		t.Fatal("undetermined probe must not fire WORKER_IMPLEMENT_NO_ARTIFACT")
	}

	determined := oar.NewGuardContext()
	workercompletion.ObserveImplementerFinishWithoutWrite(
		context.Background(), child, msgs, "I attempted to edit game.py.",
		child.WorkspacePath, stubWorkspaceChecker{changed: false}, determined,
	)
	if _, rejected := determined.RejectData["WORKER_IMPLEMENT_NO_ARTIFACT"]; !rejected {
		t.Fatal("a probe that ran and found a clean workspace must still reject")
	}
}
