package workflow

import (
	"context"
	"maps"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/pkg/api"
)

type reviewContractError struct{ error }

const reviewContractInvalidCode = "WORKFLOW_REVIEW_CONTRACT_INVALID"

func (m *RunManager) blockReviewContract(ctx context.Context, runID string, cause error) error {
	unlock := m.lockRunVars(runID)
	defer unlock()
	run, err := m.loadRun(ctx, runID)
	if err != nil {
		return err
	}
	if run.Status != api.WorkflowRunStatusRunning {
		return nil
	}
	vars, err := m.Store.GetScaffoldVars(ctx, runID)
	if err != nil {
		return err
	}
	repairs, err := readReviewRepairs(vars)
	if err != nil {
		return err
	}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return err
	}
	snapshot := m.captureReviewSnapshot(ctx, run, manifest, vars, nil)
	now := time.Now().UTC()
	repair := ReviewRepair{Snapshot: &snapshot, ID: uuid.NewString(), Phase: run.CurrentPhase, State: "blocked", CreatedAt: now, UpdatedAt: now, Diagnostics: []api.ToolFeedback{{Code: reviewContractInvalidCode, Details: map[string]any{"reason": cause.Error()}}}}
	if len(snapshot.Unavailable) == 0 {
		facts := BuildCoverageFacts(manifest, vars, snapshot.Workers, snapshot.Scans)
		repair.CoverageFacts = &facts
	}
	repairs = append(repairs, repair)

	vars = maps.Clone(vars)
	vars[reviewRepairsKey] = repairs
	run.Status = api.WorkflowRunStatusPaused
	run.PauseReason = ReviewBlockedReason
	run.PausedAt = &now
	run.UpdatedAt = now
	boundary := newCommandBoundary(run, run.Revision, "paused", run.CurrentPhase, ReviewBlockedReason)
	if err = m.commitCommand(ctx, run, "review_contract_blocked", struct{ Code string }{reviewContractInvalidCode}, vars, &boundary, "", workflowWorkerMutation{HoldPending: true}, nil); err != nil {
		return err
	}
	m.publishSession(ctx, run)
	return nil
}
