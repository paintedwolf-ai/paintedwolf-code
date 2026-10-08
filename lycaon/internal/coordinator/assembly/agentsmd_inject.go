package assembly

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/governance"
	"github.com/lycaon/lycaon/pkg/api"
)

// agentsMDIndexInject keeps the session index in every standing prompt prefix.
func (e *turnContextAssembler) agentsMDIndexInject(ctx context.Context, sess *api.Session, turn *TurnAssemblyScratch) []api.Message {
	if e == nil || sess == nil || turn == nil {
		return nil
	}
	deps := e.surface.wiring
	if deps.AgentsMDIndex == nil {
		return nil
	}
	block, ok := deps.AgentsMDIndex(ctx, sess)
	if !ok || strings.TrimSpace(block.Content) == "" {
		return nil
	}
	block.ContextPinned = true
	return []api.Message{block}
}

// agentsMDChainInject follows the latest tool path and stays outside the cached prefix.
func (e *turnContextAssembler) agentsMDChainInject(ctx context.Context, sess *api.Session, history []api.Message, turn *TurnAssemblyScratch) []api.Message {
	if e == nil || sess == nil || turn == nil {
		return nil
	}
	deps := e.surface.wiring
	if deps.AgentsMDChain == nil {
		return nil
	}
	paths := governance.ExtractPathScopedToolPaths(history)
	relPath := governance.FirstConcreteScopePath(paths)
	block, err := deps.AgentsMDChain(ctx, sess, relPath)
	if err != nil || strings.TrimSpace(block.Content) == "" {
		return nil
	}
	return []api.Message{block}
}
