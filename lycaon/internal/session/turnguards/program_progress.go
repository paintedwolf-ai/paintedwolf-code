package turnguards

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/progress"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProgramProgressState supplies the same live owner/workflow facts to reached
// native children that the retained coordinator gate uses before tool dispatch.
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
	state := m.progressPrerequisites(ctx, sess)
	return state, ctx.Err()
}

func (m *Service) progressPrerequisites(ctx context.Context, sess *api.Session) progress.PrerequisiteState {
	state := progress.PrerequisiteState{SessionID: sess.ID, RootSessionID: sessiontree.RootID(ctx, m.store, sess.ID), WorkerChild: sess.IsWorkerChild()}
	if m.progress != nil {
		state.Content = m.progress.Get(ctx, state.RootSessionID)
	}
	state.ReviewLoopActive = m.workflows != nil && m.workflows.Policy != nil && m.workflows.Policy.ActivePhaseHasReviewLoop(ctx, sess.ID)
	return state
}
