package workercompletion_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// stubWorkspaceChecker answers the workspace probe without shelling out to git.
type stubWorkspaceChecker struct {
	changed bool
	err     error
}

func (s stubWorkspaceChecker) HasChanges(context.Context, string, string) (bool, error) {
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

func TestWorkerArtifactUsesObservedBaselineInsteadOfPrimaryDirt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changes workercompletion.WorkspaceChangeObservation
		want    api.CitationGroundingCheckStatus
		code    string
	}{
		{"changed worker", workercompletion.WorkspaceChangeObservation{Paths: []string{"material.txt"}}, api.CitationGroundingCheckStatusPassed, ""},
		{"unchanged worker", workercompletion.WorkspaceChangeObservation{}, api.CitationGroundingCheckStatusFailed, "WORKER_SUMMARY_NO_ARTIFACT"},
		{"unavailable baseline", workercompletion.WorkspaceChangeObservation{Err: errors.New("baseline unavailable")}, api.CitationGroundingCheckStatusAdvisory, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eval, err := workercompletion.EvaluateWorkerSummary(t.Context(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
				AgentType:     orchestration.ProfileImplementer,
				Report:        workercompletion.WorkerCompletionReport{LegStatus: "complete", Brief: "Finished the change.", FilesModified: []string{"claimed.txt"}},
				ChildMessages: commandRanMessages(), ProjectDir: t.TempDir(),
				WorkspaceCheck: stubWorkspaceChecker{changed: true}, WorkspaceChanges: &tc.changes,
			}))
			testutil.FailErr(t, "evaluate worker baseline", err)
			if check := artifactCheck(t, eval.Grounding); check.Status != tc.want || eval.HintCode != tc.code {
				t.Fatalf("artifact check=%+v code=%s, want %s/%s", check, eval.HintCode, tc.want, tc.code)
			}
		})
	}
}

type fakeArtifactGit struct {
	git.GitManager
	status      *git.GitStatus
	statusErr   error
	compare     git.GitComparison
	compareErr  error
	compareBase string
	compareHead string
}

func (f *fakeArtifactGit) Status(context.Context, string) (*git.GitStatus, error) {
	return f.status, f.statusErr
}

func (f *fakeArtifactGit) Compare(_ context.Context, _, base, head string) (git.GitComparison, error) {
	f.compareBase = base
	f.compareHead = head
	return f.compare, f.compareErr
}

func mutationReceiptMessages() []api.Message {
	return []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "tc-edit", Name: "edit", Args: map[string]any{"path": "main.go"}},
			},
		},
		{
			Role:       api.MessageRoleTool,
			Content:    "edited main.go",
			ToolResult: &api.ToolResult{ToolCallID: "tc-edit", Outcome: api.ToolResultOutcomeCompleted},
		},
	}
}

// TestImplementerArtifactProofInvariant verifies the three-valued artifact proof
// contract across all six decision cases plus missing base revision.
func TestImplementerArtifactProofInvariant(t *testing.T) {
	const (
		baseOID = "1111111111111111111111111111111111111111"
		headOID = "2222222222222222222222222222222222222222"
	)
	nonEmptyDir := t.TempDir()
	testutil.FailErr(t, "seed non-empty workspace file", os.WriteFile(filepath.Join(nonEmptyDir, "existing.go"), []byte("package existing\n"), 0o644))

	cases := []struct {
		name            string
		msgs            []api.Message
		baseRevision    string
		gitStub         *fakeArtifactGit
		wantStatus      string
		wantHintCode    string
		wantCheckStatus api.CitationGroundingCheckStatus
		wantCompareBase string
	}{
		{
			name:         "clean with no receipts",
			msgs:         commandRanMessages(),
			baseRevision: baseOID,
			gitStub: &fakeArtifactGit{
				status:  &git.GitStatus{Dirty: false},
				compare: git.GitComparison{BaseOID: baseOID, HeadOID: baseOID},
			},
			wantStatus:      "partial",
			wantHintCode:    workercompletion.WorkerSummaryNoArtifactCode,
			wantCheckStatus: api.CitationGroundingCheckStatusFailed,
			wantCompareBase: baseOID,
		},
		{
			name:         "uncommitted edit",
			msgs:         commandRanMessages(),
			baseRevision: baseOID,
			gitStub: &fakeArtifactGit{
				status: &git.GitStatus{Dirty: true},
			},
			wantStatus:      "complete",
			wantHintCode:    "",
			wantCheckStatus: api.CitationGroundingCheckStatusPassed,
		},
		{
			name:         "commit since base",
			msgs:         commandRanMessages(),
			baseRevision: baseOID,
			gitStub: &fakeArtifactGit{
				status:  &git.GitStatus{Dirty: false},
				compare: git.GitComparison{BaseOID: baseOID, HeadOID: headOID, Ahead: 1},
			},
			wantStatus:      "complete",
			wantHintCode:    "",
			wantCheckStatus: api.CitationGroundingCheckStatusPassed,
			wantCompareBase: baseOID,
		},
		{
			name:         "mutation receipt with clean tree",
			msgs:         mutationReceiptMessages(),
			baseRevision: baseOID,
			gitStub: &fakeArtifactGit{
				status:  &git.GitStatus{Dirty: false},
				compare: git.GitComparison{BaseOID: baseOID, HeadOID: baseOID},
			},
			wantStatus:      "complete",
			wantHintCode:    "",
			wantCheckStatus: api.CitationGroundingCheckStatusPassed,
		},
		{
			name:         "not a git repository",
			msgs:         commandRanMessages(),
			baseRevision: "",
			gitStub: &fakeArtifactGit{
				statusErr: git.ErrNotRepository,
			},
			wantStatus:      "complete",
			wantHintCode:    "",
			wantCheckStatus: api.CitationGroundingCheckStatusAdvisory,
		},
		{
			name:         "git failing on status",
			msgs:         commandRanMessages(),
			baseRevision: baseOID,
			gitStub: &fakeArtifactGit{
				statusErr: errors.New("git status: subprocess failed"),
			},
			wantStatus:      "complete",
			wantHintCode:    "",
			wantCheckStatus: api.CitationGroundingCheckStatusAdvisory,
		},
		{
			name:         "git failing on compare",
			msgs:         commandRanMessages(),
			baseRevision: baseOID,
			gitStub: &fakeArtifactGit{
				status:     &git.GitStatus{Dirty: false},
				compareErr: errors.New("git rev-list failed"),
			},
			wantStatus:      "complete",
			wantHintCode:    "",
			wantCheckStatus: api.CitationGroundingCheckStatusAdvisory,
			wantCompareBase: baseOID,
		},
		{
			name:         "clean git tree with no recorded base",
			msgs:         commandRanMessages(),
			baseRevision: "",
			gitStub: &fakeArtifactGit{
				status: &git.GitStatus{Dirty: false},
			},
			wantStatus:      "complete",
			wantHintCode:    "",
			wantCheckStatus: api.CitationGroundingCheckStatusAdvisory,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checker := &workercompletion.GitWorkspaceChangeChecker{Git: tc.gitStub}
			eval, err := workercompletion.EvaluateWorkerSummary(t.Context(), workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{
				AgentType:      orchestration.ProfileImplementer,
				Report:         workercompletion.WorkerCompletionReport{LegStatus: "complete", Brief: "Completed requested change."},
				ChildMessages:  tc.msgs,
				ProjectDir:     nonEmptyDir,
				BaseRevision:   tc.baseRevision,
				WorkspaceCheck: checker,
			}))
			testutil.FailErr(t, "evaluate implementer artifact proof", err)

			if eval.Status != tc.wantStatus || eval.HintCode != tc.wantHintCode {
				t.Fatalf("status=%q hint=%q, want %q/%q (eval=%+v)", eval.Status, eval.HintCode, tc.wantStatus, tc.wantHintCode, eval)
			}
			if check := artifactCheck(t, eval.Grounding); check.Status != tc.wantCheckStatus {
				t.Fatalf("implementer_artifact status=%q, want %q (check=%+v)", check.Status, tc.wantCheckStatus, check)
			}
			if tc.wantCompareBase != "" && (tc.gitStub.compareBase != tc.wantCompareBase || tc.gitStub.compareHead != "HEAD") {
				t.Fatalf("Compare(%q, %q), want Compare(%q, \"HEAD\")", tc.gitStub.compareBase, tc.gitStub.compareHead, tc.wantCompareBase)
			}
		})
	}
}
