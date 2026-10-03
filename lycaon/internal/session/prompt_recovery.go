package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

// ErrPromptRecoveryStale rejects recovery after the interrupted work has changed.
var ErrPromptRecoveryStale = errors.New("the interrupted turn has changed; refresh the chat before continuing")

// preparePromptRecovery binds a user action to the latest unsettled outcome.
// The existing receipt JSON retains this relationship across process restarts.
func (m *Manager) preparePromptRecovery(ctx context.Context, sessionID string, in PromptInput) (PromptInput, error) {
	action := in.Recovery
	if action.Action != "continue" && action.Action != "retry" {
		return in, ErrPromptRecoveryStale
	}
	if len(in.ArtifactIDs) != 0 || len(in.ContentParts) != 0 || in.SourceContext != nil {
		return in, ErrPromptRecoveryStale
	}
	state, err := m.store.ReadExecutionState(ctx, sessionID)
	if err != nil {
		return in, err
	}
	var failedID string
	for _, s := range state.Sessions {
		if s.ID == sessionID && s.Status == api.SessionStatusBusy {
			return in, ErrPromptRecoveryStale
		}
	}
	for _, s := range state.Submissions {
		if s.SessionID != sessionID {
			continue
		}
		switch s.Status {
		case store.PromptSubmissionFailed, store.PromptSubmissionInterrupted, store.PromptSubmissionCanceled:
			failedID = s.ID
		default:
			return in, ErrPromptRecoveryStale
		}
	}
	if failedID == "" {
		return in, ErrPromptRecoveryStale
	}
	history, err := m.store.GetMessages(ctx, sessionID)
	if err != nil {
		return in, err
	}
	latest := ""
	for _, msg := range history {
		if !api.IsInternalTranscriptMessage(msg) {
			latest = msg.ID
		}
	}
	boundary := api.UserIntentBoundary(history)
	if latest == "" || latest != action.AfterMessageID || boundary == 0 {
		return in, ErrPromptRecoveryStale
	}
	if action.Action == "retry" {
		for _, msg := range history[boundary:] {
			if msg.Role == api.MessageRoleAssistant || msg.Role == api.MessageRoleTool || len(msg.ToolCalls) != 0 {
				return in, ErrPromptRecoveryStale
			}
		}
		original, err := m.store.GetPromptSubmission(ctx, history[boundary-1].ID)
		if err != nil {
			return in, err
		}
		if err := json.Unmarshal([]byte(original.InputJSON), &in); err != nil {
			return in, fmt.Errorf("decode interrupted request: %w", err)
		}
		in.Recovery = action
	} else {
		in.Text = "Keep going"
	}
	in.Continuation = true
	in.ResumesSubmissionID = failedID
	return in, nil
}
