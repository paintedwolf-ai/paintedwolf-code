package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

type Store interface {
	ReadExecutionState(context.Context, string) (store.ExecutionState, error)
	GetMessages(context.Context, string) ([]api.Message, error)
	GetPromptSubmission(context.Context, string) (*store.PromptSubmission, error)
}
type Service struct{ store Store }

func New(store Store) *Service { return &Service{store: store} }

// ErrStale rejects a recovery action whose premise no longer
// holds. Each refusal wraps it with the fact that failed, in the person's terms;
// the HTTP layer answers with prompt_recovery_stale and that reason.
var ErrStale = errors.New("prompt recovery is stale")

type staleError struct{ reason string }

func (e *staleError) Error() string { return e.reason }
func (e *staleError) Unwrap() error { return ErrStale }

func staleRecovery(reason string) error { return &staleError{reason: reason} }

// Prepare binds the action to the interrupted turn and transcript.
func (m *Service) Prepare(ctx context.Context, sessionID string, in promptinput.Input) (promptinput.Input, error) {
	action := in.Recovery
	if action.Action != "continue" && action.Action != "retry" {
		return in, staleRecovery(fmt.Sprintf("%q is not a recovery action", action.Action))
	}
	if len(in.ArtifactIDs) != 0 || len(in.ContentParts) != 0 || in.SourceContext != nil {
		return in, staleRecovery("Recovery cannot add attachments, references, or source context; send them as a new message")
	}
	state, err := m.store.ReadExecutionState(ctx, sessionID)
	if err != nil {
		return in, err
	}
	recoverable := false
	for _, s := range state.Sessions {
		if s.ID == sessionID && s.Status == api.SessionStatusBusy {
			return in, staleRecovery("A turn is still running in this chat")
		}
	}
	for _, s := range state.Submissions {
		if s.SessionID != sessionID {
			continue
		}
		switch s.Status {
		case store.PromptSubmissionFailed, store.PromptSubmissionInterrupted, store.PromptSubmissionCanceled:
			recoverable = true
		default:
			return in, staleRecovery("Another prompt is already queued or running in this chat")
		}
	}
	if !recoverable {
		return in, staleRecovery("The last turn finished on its own; send a new message to carry on")
	}
	history, err := m.store.GetMessages(ctx, sessionID)
	if err != nil {
		return in, err
	}
	// The client anchors on the last message it rendered. A host-written
	// internal trailer after the last visible message is the same point in
	// the conversation, so either id names the state the person saw.
	latest, latestVisible := "", ""
	for _, msg := range history {
		latest = msg.ID
		if !api.IsInternalTranscriptMessage(msg) {
			latestVisible = msg.ID
		}
	}
	boundary := api.UserIntentBoundary(history)
	if boundary == 0 {
		return in, staleRecovery("No turn has started in this chat")
	}
	if latestVisible == "" || (action.AfterMessageID != latest && action.AfterMessageID != latestVisible) {
		return in, staleRecovery("The chat has moved on since this notice; refresh it and choose again")
	}
	if action.Action == "retry" {
		for _, msg := range history[boundary:] {
			if msg.Role == api.MessageRoleAssistant || msg.Role == api.MessageRoleTool || len(msg.ToolCalls) != 0 {
				return in, staleRecovery("The interrupted turn already made progress; use Keep going instead of Retry")
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
	return in, nil
}
