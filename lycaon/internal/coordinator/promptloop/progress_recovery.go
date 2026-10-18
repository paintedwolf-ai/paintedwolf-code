package promptloop

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

func appendProgressRecovery(ctx context.Context, sess *api.Session, profileID string, messages []api.Message, render func(context.Context, *api.Session) (string, error)) ([]api.Message, error) {
	if profileID != prompts.CoordinatorProfileID || render == nil {
		return messages, nil
	}
	recovery := transcript.ProgressRecovery(messages)
	if len(recovery) == 0 {
		return messages, nil
	}
	block, err := render(ctx, sess)
	if err != nil {
		return nil, fmt.Errorf("render progress recovery: %w", err)
	}
	if strings.TrimSpace(block) == "" {
		return messages, nil
	}
	parts := append([]api.MessageContentPart{{Content: block, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted}}, recovery...)
	out := append([]api.Message(nil), messages...)
	return append(out, api.Message{ID: "host-progress-recovery", Role: api.MessageRoleSystem, Content: block, ContentParts: parts, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted, ContextPinned: true}), nil
}
