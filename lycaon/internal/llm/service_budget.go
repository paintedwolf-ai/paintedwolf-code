package llm

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

// RequestBudget admits measurement before it waits on a serialized runtime.
func (s *Service) RequestBudget(ctx context.Context, providerID string, req modelcall.CompletionRequest) modelcall.RequestBudget {
	var registry *Registry
	if s != nil {
		registry = s.Registry
	}
	if registry != nil {
		if provider, err := registry.Get(providerID); err == nil {
			release, err := s.Utility.acquireForeground(ctx, provider)
			if err != nil {
				return modelcall.RequestBudget{Err: err}
			}
			defer release()
		}
	}
	return registry.RequestBudget(ctx, providerID, req)
}
