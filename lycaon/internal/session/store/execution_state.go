package store

import (
	"context"
	"sort"

	"github.com/lycaon/lycaon/pkg/api"
)

type ExecutionSession struct {
	ID        string            `json:"id"`
	ProjectID string            `json:"project_id"`
	Status    api.SessionStatus `json:"status"`
}

type ExecutionSubmission struct {
	ID        string                 `json:"id"`
	SessionID string                 `json:"session_id"`
	Status    PromptSubmissionStatus `json:"status"`
	ErrorCode string                 `json:"error_code,omitempty"`
}

type ExecutionState struct {
	Sessions    []ExecutionSession
	Submissions []ExecutionSubmission
}

// ReadExecutionState reads occupancy and failures from one store snapshot.
func (s *SQL) ReadExecutionState(ctx context.Context, root string) (ExecutionState, error) {
	var state ExecutionState
	rows, err := s.queries.ReadExecutionState(ctx, root)
	if err != nil {
		return state, err
	}
	for _, row := range rows {
		if row.Kind == "session" {
			state.Sessions = append(state.Sessions, ExecutionSession{ID: row.ID, ProjectID: row.ProjectID, Status: api.SessionStatus(row.Status)})
		} else {
			state.Submissions = append(state.Submissions, ExecutionSubmission{ID: row.ID, SessionID: row.SessionID, Status: PromptSubmissionStatus(row.Status), ErrorCode: row.ErrorCode})
		}
	}
	return state, nil
}

func (s *Memory) ReadExecutionState(ctx context.Context, root string) (ExecutionState, error) {
	var state ExecutionState
	if err := ctx.Err(); err != nil {
		return state, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	scope := map[string]bool{}
	for _, id := range memorySessionTreeIDs(s.sessions, root) {
		if row := s.sessions[id]; row != nil {
			scope[id] = true
			state.Sessions = append(state.Sessions, ExecutionSession{ID: id, ProjectID: row.ProjectID, Status: row.Status})
		}
	}
	latest := map[string]int64{}
	for _, p := range s.promptSubmissions {
		if scope[p.SessionID] && p.AdmissionSeq > latest[p.SessionID] {
			latest[p.SessionID] = p.AdmissionSeq
		}
	}
	for _, p := range s.promptSubmissions {
		if scope[p.SessionID] && p.Status != PromptSubmissionComplete && (!p.Status.Terminal() || p.AdmissionSeq == latest[p.SessionID]) {
			state.Submissions = append(state.Submissions, ExecutionSubmission{ID: p.ID, SessionID: p.SessionID, Status: p.Status, ErrorCode: p.ErrorCode})
		}
	}
	sort.Slice(state.Sessions, func(i, j int) bool { return state.Sessions[i].ID < state.Sessions[j].ID })
	sort.Slice(state.Submissions, func(i, j int) bool { return state.Submissions[i].ID < state.Submissions[j].ID })
	return state, nil
}
