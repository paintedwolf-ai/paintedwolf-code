package promptloop

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

// newProvisionalAssistantMessage keeps the row internal until guards accept it.
func newProvisionalAssistantMessage(msg api.Message) api.Message {
	msg.Role = api.MessageRoleAssistant
	msg.Visibility = api.MessageVisibilityInternal
	return msg
}

func (l *PromptLoop) attachProseCitationGrounding(
	ctx context.Context,
	sess *api.Session,
	history []api.Message,
	userPrompt, surfaceID string,
	assistantMsg *api.Message,
) {
	if l == nil || l.Deps.ProseCitationGrounding == nil || assistantMsg == nil {
		return
	}
	// Host-assembled grounding retains the assembler's provenance.
	if assistantMsg.Grounding != nil && assistantMsg.Grounding.HostAssembled {
		return
	}
	if g := l.Deps.ProseCitationGrounding(ctx, sess, history, userPrompt, assistantMsg.Content, surfaceID); g != nil {
		assistantMsg.Grounding = g
	}
}

// promptTurnSurface returns the pinned surface, or empty when unresolved.
func (l *PromptLoop) promptTurnSurface(sessionID string) string {
	if l == nil || l.Deps.PromptTurnSurface == nil {
		return ""
	}
	return l.Deps.PromptTurnSurface(sessionID)
}

// closeoutWireContent projects streaming envelopes to prose for transcript rendering.
func closeoutWireContent(surfaceID string) func(string) string {
	if !surface.SurfaceDeliversReport(surfaceID) {
		return func(content string) string { return content }
	}
	return func(content string) string {
		if prose, ok := guidance.StreamingCloseoutNarrative(content); ok {
			return prose
		}
		return content
	}
}

// wireCopy projects transcript content while preserving the raw envelope in loop history.
func wireCopy(msg api.Message, project func(string) string) api.Message {
	msg.Content = project(msg.Content)
	return msg
}

// keepCloseoutEnvelopeInternal also projects the body because grounded rows can render internally.
func (l *PromptLoop) keepCloseoutEnvelopeInternal(ctx context.Context, sessionID, surfaceID string, msg api.Message) (api.Message, error) {
	msg.Visibility = api.MessageVisibilityInternal
	if l.Deps.UpdateMessage != nil {
		if err := l.Deps.UpdateMessage(ctx, sessionID, msg.ID, wireCopy(msg, closeoutWireContent(surfaceID))); err != nil {
			return api.Message{}, err
		}
	}
	return msg, nil
}

// commitProvisionalAssistantTurn promotes a guarded assistant row to the user transcript.
func (l *PromptLoop) commitProvisionalAssistantTurn(ctx context.Context, sessionID string, msg api.Message) (api.Message, error) {
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

// commitGuardedAssistantTurn grounds the raw envelope before publishing its narrative.
func (l *PromptLoop) commitGuardedAssistantTurn(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	history []api.Message,
	userPrompt, surfaceID string,
	msg api.Message,
) (api.Message, error) {
	coordinatorSession := sess == nil || strings.TrimSpace(sess.ParentSessionID) == ""
	// Only prose responses become user drafts; tool-step text stays in model history.
	if len(msg.ToolCalls) == 0 {
		if surface.SurfaceDeliversReport(surfaceID) {
			l.attachProseCitationGrounding(ctx, sess, history, userPrompt, surfaceID, &msg)
			if prose, ok := guard.ProjectCoordinatorCloseoutProse(surfaceID, msg.Content); ok {
				msg.Content = prose
			} else if guidance.CloseoutBodyIsEnvelopeShaped(msg.Content) {
				// Unprojectable envelopes remain internal for retry.
				return l.keepCloseoutEnvelopeInternal(ctx, sessionID, surfaceID, msg)
			} else if coordinatorSession && msg.Grounding == nil && strings.TrimSpace(msg.Content) != "" {
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
		if err := (turnNudges{l}).stampDraftVersionCount(ctx, sessionID, &msg); err != nil {
			return api.Message{}, err
		}
		// Retried tool-only steps retain the draft card's version history.
		if midRunStep && msg.Kind != api.MessageKindDraft && msg.DraftVersionCount > 1 {
			msg.Kind = api.MessageKindDraft
		}
	}
	return l.commitProvisionalAssistantTurn(ctx, sessionID, msg)
}

// commitProvisionalAssistantInHistory promotes the assistant row in loop history once guards pass.
func (l *PromptLoop) commitProvisionalAssistantInHistory(
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
