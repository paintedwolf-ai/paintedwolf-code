package review_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowreview "github.com/lycaon/lycaon/internal/workflow/review"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

type interruptedVerdictCommit struct {
	runstate.VerdictsRepository
	evidence  inspector.EvidenceStore
	directory string
	published bool
}

func (s interruptedVerdictCommit) CommitVerdictOperation(ctx context.Context, op runstate.VerdictOperation, run *api.WorkflowRun, projectDir string, vars map[string]any, out runstate.ReviewOutcome) error {
	if s.published {
		record := evidence.GateRecord(evidence.GateType("rl_key"), "judge", run.ID, evidence.GateVerdictNeedsChanges, "NEEDS_REVISION", map[string]any{
			"verdict": "NEEDS_REVISION", "winner": "pending",
			evidence.CitedEvidenceArtifactKey: []any{map[string]any{"handle": "read#1", "path": "file.go", "line": 1, "excerpt": "example"}},
			evidence.CitedURLsArtifactKey:     []any{},
		}, "", "", "", 0, op.CreatedAt.UTC())
		record.RecordID = op.EvidenceRecordID
		if err := s.evidence.Append(ctx, s.directory, record); err != nil {
			return err
		}
	}
	return errors.New("injected interruption before verdict commit")
}

func TestPreparedVerdictRecoveryPreservesJudgedRound(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(map[bool]string{false: "before publication", true: "after publication"}[published], func(t *testing.T) {
			mgr, _, blueprints, _ := testManager(t)
			setTestRegistry(t, mgr, blueprints, conditions.TestRegistryDeps())
			run := startReviewLoopRun(t.Context(), t, mgr)
			directory := t.TempDir()
			ledger := inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
			mgr.Verdicts.EvidenceStore = ledger
			mgr.SetEvidenceProjectDir(func(context.Context, string) (string, error) { return directory, nil })
			records := mgr.Verdicts.Records
			mgr.Verdicts.Records = interruptedVerdictCommit{records, ledger, directory, published}
			ctx := workflowreview.WithOperationID(t.Context(), "interrupted-verdict")
			_, err := mgr.Verdicts.RecordReviewLoopVerdict(ctx, run.SessionID, map[string]string{"verdict": "NEEDS_REVISION", "winner": "pending"}, []api.CitationGroundingCitedEvidence{{Handle: "read#1", Path: "file.go", Line: 1, Excerpt: "example"}}, nil)
			if err == nil {
				t.Fatal("expected interruption")
			}
			mgr.Verdicts.Records = records
			for boot := 0; boot < 2; boot++ {
				testutil.FailErr(t, "recover verdict", mgr.Verdicts.RecoverVerdictOperations(t.Context()))
				pending, err := records.PendingVerdictOperations(t.Context())
				testutil.FailErr(t, "read pending verdicts", err)
				if len(pending) != 0 {
					t.Fatalf("recovery left pending receipts: %+v", pending)
				}
			}
			values, err := mgr.Store.Runs.GetScaffoldVars(t.Context(), run.ID)
			testutil.FailErr(t, "read review round", err)
			if got := runstate.ReviewLoopAttempt(values, "judge"); got != 1 {
				t.Fatalf("round=%d, want 1", got)
			}
			all, err := ledger.ReadAll(t.Context(), directory, run.ID, "judge", evidence.GateType("rl_key"))
			testutil.FailErr(t, "read evidence", err)
			if len(all) != 1 || all[0].Attempt != 0 {
				t.Fatalf("judged round changed: %+v", all)
			}
		})
	}
}
