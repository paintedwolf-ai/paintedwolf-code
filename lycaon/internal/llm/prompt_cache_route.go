package llm

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

// ModelResidencyReporter is a local driver that can say whether its runner
// still holds a model, and with it the KV state of the last prefix served.
type ModelResidencyReporter interface {
	ModelResident(ctx context.Context, model string) (resident, known bool)
}

// PromptCachePolicy returns the prompt-cache policy one route runs under:
// the provider kind's policy refined for the model. ok is false for an
// unknown provider.
func (r *Registry) PromptCachePolicy(providerID, model string) (providerprofile.PromptCachePolicy, bool) {
	if r == nil {
		return providerprofile.PromptCachePolicy{}, false
	}
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return providerprofile.PromptCachePolicy{}, false
	}
	entry, ok := snapshot.entries[strings.TrimSpace(providerID)]
	if !ok {
		return providerprofile.PromptCachePolicy{}, false
	}
	return entry.PromptCache.ForModel(model), true
}

// ModelResident asks a route's local runner whether it still holds the
// model. known is false when the driver has no probe or the runner cannot say.
func (r *Registry) ModelResident(ctx context.Context, providerID, model string) (resident, known bool) {
	if r == nil {
		return false, false
	}
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return false, false
	}
	reporter, ok := snapshot.providers[strings.TrimSpace(providerID)].(ModelResidencyReporter)
	if !ok {
		return false, false
	}
	return reporter.ModelResident(ctx, model)
}
