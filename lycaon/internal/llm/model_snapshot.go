package llm

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

// resolveSelectedModel uses the current catalog for a known model. Unknown
// selections still require discovery before role and capability validation.
func (r *Registry) resolveSelectedModel(ctx context.Context, providerID, model string) mergeResult {
	result, refresh := r.modelSnapshot(ctx, providerID)
	kind, _ := r.ProviderKind(providerID)
	for _, entry := range result.Models {
		if modelinfo.EquivalentID(kind, entry.ID, model) {
			refresh()
			return result
		}
	}
	return r.resolveModels(ctx, providerID)
}

func (r *Registry) modelSnapshot(ctx context.Context, providerID string) (mergeResult, func()) {
	noop := func() {}
	snapshot := r.snapshot.Load()
	if snapshot == nil || !snapshot.configured[providerID] {
		return mergeResult{DiscoveryStatus: DiscoveryStatusSkipped}, noop
	}
	entry, ok := snapshot.entries[providerID]
	if !ok {
		return mergeResult{DiscoveryStatus: DiscoveryStatusSkipped}, noop
	}
	profile := providerprofile.Default()
	if provider := snapshot.providers[providerID]; provider != nil {
		profile = provider.Profile()
	}
	key := snapshot.credentialValues[providerID]
	cacheKey := discoveryCacheKey(providerID, profile, entry.BaseURL, key)
	var models []modelinfo.Entry
	var lastErr error
	fresh := false
	if r.discovery != nil {
		models, _, fresh, lastErr = r.discovery.Snapshot(cacheKey)
	}
	feed, status, usable := r.feedSnapshot()
	// A transient refresh failure does not erase the last successful catalog.
	result := mergeAssignableModels(entry.Kind, entry.Models, models, nil, feed, status, usable)
	if lastErr != nil {
		result.DiscoveryError = lastErr
		result.DiscoveryStatus = DiscoveryStatusError
	}
	r.publishHydratedProviders(snapshot, map[string][]modelinfo.Entry{providerID: result.Models})
	if fresh || r.discovery == nil || profile.Discovery == providerprofile.DiscoveryNone {
		return result, noop
	}
	return result, func() {
		r.discovery.Refresh(ctx, cacheKey, func(ctx context.Context) ([]modelinfo.Entry, error) {
			return fetchDiscoveredModels(ctx, r.discoveryClient, profile, entry.BaseURL, key)
		})
	}
}
