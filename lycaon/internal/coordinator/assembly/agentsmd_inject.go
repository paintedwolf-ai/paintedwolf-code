package assembly

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// agentsMDIndexInject keeps the session index in every standing prompt prefix.
func (e *turnContextAssembler) agentsMDIndexInject(ctx context.Context, sess *api.Session, turn *TurnAssemblyScratch) []api.Message {
	if e == nil || sess == nil || turn == nil {
		return nil
	}
	deps := e.deps
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

// agentsMDChainInject keeps current working guidance outside the cached prefix.
func (e *turnContextAssembler) agentsMDChainInject(ctx context.Context, sess *api.Session, history []api.Message, turn *TurnAssemblyScratch) ([]api.Message, error) {
	if e == nil || sess == nil || turn == nil || e.deps.AgentsMDPolicies == nil {
		return nil, nil
	}
	return e.deps.AgentsMDPolicies(ctx, sess, history)
}
