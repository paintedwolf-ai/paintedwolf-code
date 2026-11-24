package turnguards

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/progress"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProgramProgressState supplies current progress and workflow facts to reached operations.
func (m *Service) ProgramProgressState(ctx context.Context, sessionID string) (progress.PrerequisiteState, error) {
	if err := ctx.Err(); err != nil {
		return progress.PrerequisiteState{}, err
	}
	if m == nil || m.store == nil {
		return progress.PrerequisiteState{}, fmt.Errorf("session progress facts unavailable")
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return progress.PrerequisiteState{}, err
	}
	if sess == nil || sess.ID != sessionID {
		return progress.PrerequisiteState{}, fmt.Errorf("session progress identity unavailable")
	}
	state, _ := m.progressPrerequisitesWithSnapshot(ctx, sess)
	return state, ctx.Err()
}

func (m *Service) progressPrerequisitesWithSnapshot(ctx context.Context, sess *api.Session) (progress.PrerequisiteState, progress.Snapshot) {
	root := sessiontree.RootID(ctx, m.store, sess.ID)
	var snapshot progress.Snapshot
	if m.progress != nil {
		snapshot, _ = m.progress.Snapshot(ctx, root)
	}
	state := progress.PrerequisiteState{
		SessionID:        sess.ID,
		RootSessionID:    root,
		Missing:          snapshot.Missing(),
		OpenSteps:        snapshot.OpenStepCount(),
		WorkerChild:      sess.IsWorkerChild(),
		ReviewLoopActive: m.workflows != nil && m.workflows.Policy != nil && m.workflows.Policy.ActivePhaseHasReviewLoop(ctx, sess.ID),
	}
	if m.ProgressClosure != nil {
		state.Reconciliation = m.ProgressClosure.Reconciliation(root)
	}
	return state, snapshot
}
