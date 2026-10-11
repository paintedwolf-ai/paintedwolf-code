package promptloop

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func (l *toolInvocations) appendToolProcedures(
	ctx context.Context, sess *api.Session, profileID string,
	messages []api.Message, offered []tools.ToolMeta,
) ([]api.Message, error) {
	if l.Context.Deps.ToolProcedures == nil || len(offered) == 0 {
		return messages, nil
	}
	names := make([]string, 0, len(offered))
	for _, meta := range offered {
		names = append(names, meta.Name)
	}
	block, err := l.Context.Deps.ToolProcedures(ctx, sess, profileID, names)
	if err != nil {
		return nil, fmt.Errorf("render offered tool procedures: %w", err)
	}
	if strings.TrimSpace(block) == "" {
		return messages, nil
	}
	// The block follows the offered schemas, so it closes the standing tier.
	index := providerwire.SystemPreambleEnd(messages)
	procedures := api.Message{
		ID:   "host-tool-procedures",
		Role: api.MessageRoleSystem, Content: block,
		Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted,
		ContextPinned: true,
	}
	out := make([]api.Message, 0, len(messages)+1)
	out = append(out, messages[:index]...)
	if index > 0 && out[index-1].PromptCacheBreakpoint == api.PromptCacheTierStanding {
		out[index-1].PromptCacheBreakpoint = api.PromptCacheTierNone
		procedures.PromptCacheBreakpoint = api.PromptCacheTierStanding
	}
	out = append(out, procedures)
	return append(out, messages[index:]...), nil
}
