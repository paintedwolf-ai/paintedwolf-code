package promptloop

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/trainingcapture"
	"github.com/lycaon/lycaon/pkg/api"
)

// newProvisionalAssistantMessage keeps the row internal until guards accept it.
func newProvisionalAssistantMessage(msg api.Message) api.Message {
	msg.Role = api.MessageRoleAssistant
	msg.Visibility = api.MessageVisibilityInternal
	return msg
}

// promptTurnSurface returns the pinned surface, or empty when unresolved.
func (l *promptContext) promptTurnSurface(sessionID string) string {
	if l == nil || l.Deps.PromptTurnSurface == nil {
		return ""
	}
	return l.Deps.PromptTurnSurface(sessionID)
}

// keepUnansweredInternal leaves a reply with nothing to show out of the transcript.
func (l *turnProjection) keepUnansweredInternal(ctx context.Context, sessionID string, msg api.Message) (api.Message, error) {
	msg.Visibility = api.MessageVisibilityInternal
	if l.Deps.UpdateMessage != nil {
		if err := l.Deps.UpdateMessage(ctx, sessionID, msg.ID, msg); err != nil {
			return api.Message{}, err
		}
	}
	return msg, nil
}

// commitProvisionalAssistantTurn promotes a guarded assistant row to the user transcript.
func (l *turnProjection) commitProvisionalAssistantTurn(ctx context.Context, sessionID string, msg api.Message) (api.Message, error) {
	if l == nil || l.Deps.UpdateMessage == nil {
		return api.Message{}, fmt.Errorf("update message not configured")
	}
	patch := msg
	patch.Visibility = api.MessageVisibilityTranscript
	if err := l.Deps.UpdateMessage(ctx, sessionID, msg.ID, patch); err != nil {
		return api.Message{}, err
	}
	return patch, nil
}

// commitGuardedAssistantTurn publishes a guarded row.
func (l *turnProjection) commitGuardedAssistantTurn(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	history []api.Message,
	userPrompt, surfaceID string,
	msg api.Message,
) (api.Message, error) {
	if err := l.assertCoordinatorCompletion(ctx, sess, sessionID, history, msg); err != nil {
		return api.Message{}, err
	}
	coordinatorSession := sess == nil || strings.TrimSpace(sess.ParentSessionID) == ""
	// Only prose responses become user drafts; tool-step text stays in model history.
	if len(msg.ToolCalls) == 0 {
		if surface.SurfaceDeliversReport(surfaceID) {
			// The closeout path grounds and presents every reply with an answer, so
			// what reaches here unaccepted is at most a draft, without protocol nodes.
			if coordinatorSession && msg.Grounding == nil && strings.TrimSpace(msg.Content) != "" {
				msg.Content = strings.TrimSpace(citationmd.Project(citationmd.Parse(msg.Content, citationmd.Options{})))
				if msg.Content == "" {
					return l.keepUnansweredInternal(ctx, sessionID, msg)
				}
				msg.Kind = api.MessageKindDraft
				msg.CompletionReport = nil
			}
		} else if coordinatorSession && strings.TrimSpace(msg.Content) != "" {
			msg.Kind = api.MessageKindDraft
		}
	}
	// Tool-only steps settle the live draft without adding a prose card.
	proseCommit := msg.DraftStatus == api.DraftStatusLive &&
		len(msg.ToolCalls) == 0 &&
		strings.TrimSpace(msg.Content) != ""
	midRunStep := msg.DraftStatus == api.DraftStatusLive && len(msg.ToolCalls) > 0
	if msg.Kind == api.MessageKindDraft || proseCommit || midRunStep {
		msg.DraftStatus = api.DraftStatusCommitted
		if err := l.Nudges.stampDraftVersionCount(ctx, sessionID, &msg); err != nil {
			return api.Message{}, err
		}
		// Retried tool-only steps retain the draft card's version history.
		if midRunStep && msg.Kind != api.MessageKindDraft && msg.DraftVersionCount > 1 {
			msg.Kind = api.MessageKindDraft
		}
	}
	committed, err := l.commitProvisionalAssistantTurn(ctx, sessionID, msg)
	if err == nil && len(committed.ToolCalls) == 0 && strings.TrimSpace(committed.Content) != "" {
		trainingcapture.TurnCommitted(ctx, sessionID, committed.ID)
	}
	return committed, err
}

// commitProvisionalAssistantInHistory promotes the assistant row in loop history once guards pass.
func (l *turnProjection) commitProvisionalAssistantInHistory(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	history []api.Message,
	userPrompt, surfaceID, assistantMessageID string,
) ([]api.Message, error) {
	assistantMessageID = strings.TrimSpace(assistantMessageID)
	if assistantMessageID == "" {
		return history, nil
	}
	for i := range history {
		if history[i].ID != assistantMessageID {
			continue
		}
		if history[i].Visibility == api.MessageVisibilityTranscript {
			return history, nil
		}
		committed, err := l.commitGuardedAssistantTurn(ctx, sess, sessionID, history, userPrompt, surfaceID, history[i])
		if err != nil {
			return nil, err
		}
		history[i] = committed
		return history, nil
	}
	return history, nil
}
