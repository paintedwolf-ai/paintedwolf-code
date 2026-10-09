package session

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

type ExecutionCheckpointSource interface {
	ListPending(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error)
}

type ExecutionBlocker struct {
	SessionID string `json:"session_id"`
	Kind      string `json:"kind"`
	ID        string `json:"id,omitempty"`
}

type ExecutionObservation struct {
	SessionID        string                       `json:"session_id"`
	SubmissionID     string                       `json:"submission_id"`
	SubmissionStatus store.PromptSubmissionStatus `json:"submission_status"`
	Settled          bool                         `json:"settled"`
	Sessions         []store.ExecutionSession     `json:"sessions"`
	Blockers         []ExecutionBlocker           `json:"blockers"`
	Failures         []store.ExecutionSubmission  `json:"failures"`
}

type WorkflowExecutionObservation struct {
	Run       api.WorkflowRun      `json:"run"`
	Execution ExecutionObservation `json:"execution"`
}

func (m *Manager) SetExecutionCheckpoints(source ExecutionCheckpointSource) {
	m.executionCheckpoints = source
}

// ObserveExecution separates runtime occupancy from unfinished task obligations.
func (m *Manager) ObserveExecution(ctx context.Context, sessionID, submissionID string) (ExecutionObservation, error) {
	result := ExecutionObservation{SessionID: sessionID, SubmissionID: submissionID, Blockers: []ExecutionBlocker{}, Failures: []store.ExecutionSubmission{}}
	if m == nil || m.store == nil {
		return result, fmt.Errorf("session manager not configured")
	}
	admission, err := m.store.GetPromptSubmission(ctx, submissionID)
	if err != nil {
		return result, err
	}
	if admission == nil {
		return result, store.ErrPromptSubmissionNotFound
	}
	if admission.SessionID != sessionID {
		return result, fmt.Errorf("submission does not belong to session")
	}
	result, err = m.ObserveExecutionTree(ctx, sessionID)
	result.SubmissionID = submissionID
	result.SubmissionStatus = admission.Status
	result.Settled = result.Settled && admission.Status.Terminal()
	return result, err
}

// ObserveExecutionTree reads occupancy without requiring a prompt admission.
func (m *Manager) ObserveExecutionTree(ctx context.Context, sessionID string) (ExecutionObservation, error) {
	result := ExecutionObservation{SessionID: sessionID, Blockers: []ExecutionBlocker{}, Failures: []store.ExecutionSubmission{}}
	if m == nil || m.store == nil {
		return result, fmt.Errorf("session manager not configured")
	}
	state, err := m.store.ReadExecutionState(ctx, sessionID)
	if err != nil {
		return result, err
	}
	if len(state.Sessions) == 0 {
		return result, fmt.Errorf("execution session no longer exists")
	}
	result.Sessions = state.Sessions
	for _, p := range state.Submissions {
		if !p.Status.Terminal() {
			result.Blockers = append(result.Blockers, ExecutionBlocker{SessionID: p.SessionID, Kind: "submission", ID: p.ID})
		} else {
			result.Failures = append(result.Failures, p)
		}
	}
	for _, s := range state.Sessions {
		blockers, err := m.executionBlockers(ctx, s)
		if err != nil {
			return result, err
		}
		result.Blockers = append(result.Blockers, blockers...)
	}
	result.Settled = len(result.Blockers) == 0
	return result, nil
}

func (m *Manager) executionBlockers(ctx context.Context, s store.ExecutionSession) ([]ExecutionBlocker, error) {
	var blockers []ExecutionBlocker
	add := func(kind, id string) {
		blockers = append(blockers, ExecutionBlocker{SessionID: s.ID, Kind: kind, ID: id})
	}
	if s.Status == api.SessionStatusBusy || s.Status == api.SessionStatusPreparing {
		add("session", s.ID)
	}
	loop := m.ensureCoordinatorRuntime().CoordinatorLoop()
	if loop.Admission.PromptExecutionActive(s.ID) || (loop.Nudges.HasPendingLoopWakes(s.ID) && !m.hostTurnBlocked(ctx, s.ID)) {
		add("continuation", s.ID)
	}
	idle, err := ParentSessionWorkerCycleIdle(ctx, m.workerQueue, s.ProjectID, s.ID, "")
	if err != nil {
		return nil, err
	}
	if !idle {
		add("worker_cycle", s.ID)
	}
	if m.executionCheckpoints != nil {
		pending, err := m.executionCheckpoints.ListPending(ctx, s.ID, nil)
		if err != nil {
			return nil, err
		}
		for _, checkpoint := range pending {
			add("checkpoint", checkpoint.ID)
		}
	}
	return blockers, nil
}
