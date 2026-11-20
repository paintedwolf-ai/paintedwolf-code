package store

import (
	"context"

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
func (s *sqlSessions) ReadExecutionState(ctx context.Context, root string) (ExecutionState, error) {
	var state ExecutionState
	rows, err := s.core.queries.ReadExecutionState(ctx, root)
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
