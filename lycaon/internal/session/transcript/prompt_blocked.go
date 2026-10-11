package transcript

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

// PreserveBlockedPrompt retains accepted input when a later spending check
// prevents execution. It does not resolve feedback or advance workflow state.
func (m *Service) PreserveBlockedPrompt(ctx context.Context, sessionID string, in promptinput.Input) error {
	if in.HostSignal != nil || (strings.TrimSpace(in.SubmissionID) == "" && len(in.SubmissionIDs) == 0) {
		return nil
	}
	author, err := m.PromptAuthor(ctx, in)
	if err != nil {
		return err
	}
	msg := api.Message{
		ID: MessageID(in), Role: api.MessageRoleUser,
		Content:       strings.TrimSpace(in.Text),
		ContentParts:  append([]api.MessageContentPart(nil), in.ContentParts...),
		SourceContext: in.SourceContext,
		ArtifactIDs:   append([]string(nil), in.ArtifactIDs...),
		Origin:        api.MessageOriginUser, Authority: api.ContentAuthorityUser,
		AuthorPersonID: author, TrustTier: api.ContentTrustTierTrusted,
		CreatedAt: time.Now().UTC(),
	}
	if in.Continuation {
		msg.Kind = api.MessageKindUserContinuation
	}
	StampUserVisibility(&msg)
	if err := m.Append(ctx, sessionID, msg); err != nil && !errors.Is(err, store.ErrDuplicateMessageID) {
		return fmt.Errorf("retain spending-blocked prompt: %w", err)
	}
	return nil
}
