package workflow

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

// publishedRun projects a run for the stream in the enriched shape REST
// returns, on a copy so publication does not mutate live run state.
func (m *RunManager) publishedRun(ctx context.Context, run *api.WorkflowRun) *api.WorkflowRun {
	if run == nil {
		return nil
	}
	projected := *run
	_ = m.AttachRunUI(ctx, &projected)
	return &projected
}

func (m *RunManager) mutationEventsOutboxed() bool {
	return m != nil && m.Store != nil && m.Store.MutationEventsOutboxed()
}

func (m *RunManager) publishSession(ctx context.Context, run *api.WorkflowRun) {
	if m == nil || m.Events == nil || run == nil {
		return
	}
	key := run.ProjectID
	if key == "" {
		if sess, err := m.Sessions.Get(ctx, run.SessionID); err == nil && sess != nil {
			key = sess.ProjectID
		}
	}
	if m.mutationEventsOutboxed() {
		m.Events.RefreshWorkflowDerived(ctx, key, run.SessionID)
		return
	}
	m.Events.PublishWorkflow(ctx, key, run.SessionID, api.WorkflowEvent{
		Event:         api.WorkflowEventKindRunUpdated,
		WorkflowID:    run.WorkflowID,
		WorkflowRunID: run.ID,
		Run:           m.publishedRun(ctx, run),
		Phase:         run.CurrentPhase,
		Status:        string(run.Status),
	})
}

func (m *RunManager) publish(ctx context.Context, sess *api.Session, run *api.WorkflowRun) {
	key := run.ProjectID
	if key == "" && sess != nil {
		key = sess.ProjectID
	}
	if m == nil || m.Events == nil {
		return
	}
	if m.mutationEventsOutboxed() {
		m.Events.RefreshWorkflowDerived(ctx, key, run.SessionID)
		return
	}
	m.Events.PublishWorkflow(ctx, key, run.SessionID, api.WorkflowEvent{
		Event:         api.WorkflowEventKindRunUpdated,
		WorkflowID:    run.WorkflowID,
		WorkflowRunID: run.ID,
		Run:           m.publishedRun(ctx, run),
		Phase:         run.CurrentPhase,
		Status:        string(run.Status),
	})
}

func (m *RunManager) publishPhaseAdvanced(ctx context.Context, run *api.WorkflowRun, previousPhase string) {
	if m == nil || run == nil {
		return
	}
	if m.OnPhaseAutoAdvanced != nil && run.Status == api.WorkflowRunStatusRunning {
		m.OnPhaseAutoAdvanced(ctx, run.SessionID, run.ID, previousPhase, run.CurrentPhase)
	}
	key := run.ProjectID
	if key == "" {
		if sess, err := m.Sessions.Get(ctx, run.SessionID); err == nil && sess != nil {
			key = sess.ProjectID
		}
	}
	if m.Events == nil {
		return
	}
	if m.mutationEventsOutboxed() {
		m.Events.RefreshWorkflowDerived(ctx, key, run.SessionID)
		return
	}
	m.Events.PublishWorkflow(ctx, key, run.SessionID, api.WorkflowEvent{
		Event:         api.WorkflowEventKindPhaseAdvanced,
		WorkflowID:    run.WorkflowID,
		WorkflowRunID: run.ID,
		Run:           m.publishedRun(ctx, run),
		PreviousPhase: previousPhase,
		Phase:         run.CurrentPhase,
		Status:        string(run.Status),
	})
}
