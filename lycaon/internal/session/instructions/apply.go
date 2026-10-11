package instructions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/pkg/api"
)

// An empty return means no user message was appended.
func (m *Service) Apply(ctx context.Context, id string, in promptinput.Input) (string, error) {
	visibleContent := strings.TrimSpace(in.Text)
	userPrompt := in.UserInstruction()
	if err := m.deliverKicks(ctx, id); err != nil {
		return "", err
	}
	if visibleContent == "" && len(in.ArtifactIDs) == 0 {
		return "", nil
	}
	if in.HostSignal == nil {
		var err error
		if in.AuthorPersonID, err = m.transcript.PromptAuthor(ctx, in); err != nil {
			return "", err
		}
	}
	if in.Continuation {
		_, err := m.transcript.AppendContinuation(ctx, id, in)
		return "", err
	}
	userMsg := api.Message{
		ID:        transcript.MessageID(in),
		Role:      api.MessageRoleUser,
		Content:   visibleContent,
		Origin:    api.MessageOriginUser,
		Authority: api.ContentAuthorityUser, TrustTier: api.ContentTrustTierTrusted,
		AuthorPersonID: in.AuthorPersonID,
		ContentParts:   append([]api.MessageContentPart(nil), in.ContentParts...),
		SourceContext:  in.SourceContext,
		ArtifactIDs:    append([]string(nil), in.ArtifactIDs...),
		CreatedAt:      time.Now().UTC(),
	}
	if in.HostSignal != nil {
		userMsg.Origin = api.MessageOriginHost
		userMsg.Authority = api.ContentAuthoritySystem
		userMsg.Kind = in.HostSignal.Kind
		userMsg.HostSignalID = in.HostSignal.ID
	}
	transcript.StampUserVisibility(&userMsg)
	if userPrompt != "" {
		m.BootstrapProgress(ctx, id, userMsg)
	}
	m.ReviewCheckpoint(ctx, id, userMsg)
	// The rewind anchor precedes the user message.
	if err := m.captures.SealPromptCheckpoint(ctx, id, userMsg.ID); err != nil {
		return "", err
	}
	if err := m.transcript.Append(ctx, id, userMsg); err != nil {
		// The admission ID makes a replayed append idempotent.
		if strings.TrimSpace(in.SubmissionID) == "" || !errors.Is(err, store.ErrDuplicateMessageID) {
			return "", err
		}
	}
	if userPrompt != "" {
		m.resetBatch(ctx, id, userMsg)
		// Only visible user intent resolves pending feedback.
		if m.workflows != nil && promptinput.VisibleIntent(userMsg) {
			if err := m.workflows.Feedback.TryResolveUserFeedback(ctx, id, userMsg.ID, userMsg.AuthorPersonID, userPrompt); err != nil {
				return "", fmt.Errorf("resolve user feedback: %w", err)
			}
		}
		if m.toolApprovalCoalesce != nil && promptinput.VisibleIntent(userMsg) {
			m.toolApprovalCoalesce.NoteUserIntentBoundary(id)
		}
		if m.gateRepeatLedger != nil && promptinput.VisibleIntent(userMsg) {
			// User intent resets per-turn repeat counts.
			m.gateRepeatLedger.NoteUserIntentBoundary(id)
		}
		if m.writeRootRuntime != nil && promptinput.VisibleIntent(userMsg) {
			// Parent intent clears write denials for its session tree.
			m.writeRootRuntime.NoteUserIntentBoundary(id)
		}
		if m.listenRuntime != nil && promptinput.VisibleIntent(userMsg) {
			m.listenRuntime.NoteUserIntentBoundary(id)
		}
		if m.loopbackRuntime != nil && promptinput.VisibleIntent(userMsg) {
			m.loopbackRuntime.NoteUserIntentBoundary(id)
		}
	}
	return userMsg.ID, nil
}
