package transcript

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

// Admission receipts retain the original sender.
func (m *Service) PromptAuthor(ctx context.Context, in promptinput.Input) (string, error) {
	if in.AuthorPersonID != "" {
		return in.AuthorPersonID, nil
	}
	author, err := people.Acting(ctx, m.store)
	if err != nil {
		return "", fmt.Errorf("resolve prompt author: %w", err)
	}
	return author.ID, nil
}

func MessageID(in promptinput.Input) string {
	if id := strings.TrimSpace(in.SubmissionID); id != "" {
		return id
	}
	if len(in.SubmissionIDs) > 0 {
		if id := strings.TrimSpace(in.SubmissionIDs[0]); id != "" {
			return id
		}
	}
	return uuid.NewString()
}

// AppendContinuation records direction inside the open visible turn.
func (m *Service) AppendContinuation(ctx context.Context, sessionID string, in promptinput.Input) (api.Message, error) {
	msg := api.Message{
		ID: MessageID(in), Role: api.MessageRoleUser,
		ContentParts:  append([]api.MessageContentPart(nil), in.ContentParts...),
		SourceContext: in.SourceContext,
		ArtifactIDs:   append([]string(nil), in.ArtifactIDs...),
		Content:       strings.TrimSpace(in.Text), Kind: api.MessageKindUserContinuation,
		Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser,
		AuthorPersonID: in.AuthorPersonID,
		TrustTier:      api.ContentTrustTierTrusted, CreatedAt: time.Now().UTC(),
	}
	StampUserVisibility(&msg)
	if err := m.Append(ctx, sessionID, msg); err != nil && !errors.Is(err, store.ErrDuplicateMessageID) {
		return api.Message{}, err
	}
	if m.workflows != nil && in.Recovery == nil && api.IsUserInstructionMessage(msg) {
		if err := m.workflows.Feedback.TryResolveUserFeedback(ctx, sessionID, msg.ID, msg.AuthorPersonID, msg.Content); err != nil {
			return api.Message{}, fmt.Errorf("resolve user feedback from continuation: %w", err)
		}
	}
	return msg, nil
}
