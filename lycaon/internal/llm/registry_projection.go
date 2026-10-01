package llm

import (
	"context"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/modelfeed"
	"github.com/lycaon/lycaon/pkg/api"
)

func modelEntriesToAPI(entries []modelinfo.Entry, kind string, exclusions RoleExclusions, profile providerprofile.Profile) []api.ProviderModelMeta {
	models := make([]api.ProviderModelMeta, 0, len(entries))
	for _, m := range entries {
		capabilities := m.EffectiveCapabilities()
		var inputNano, outputNano *int64
		if m.DiscoveredPricing != nil {
			if m.DiscoveredPricing.InputPer1K != nil {
				if n, err := cost.USDToNano(*m.DiscoveredPricing.InputPer1K); err == nil {
					inputNano = &n
				}
			}
			if m.DiscoveredPricing.OutputPer1K != nil {
				if n, err := cost.USDToNano(*m.DiscoveredPricing.OutputPer1K); err == nil {
					outputNano = &n
				}
			}
		} else {
			if m.InputPer1K > 0 {
				if n, err := cost.USDToNano(m.InputPer1K); err == nil {
					inputNano = &n
				}
			}
			if m.OutputPer1K > 0 {
				if n, err := cost.USDToNano(m.OutputPer1K); err == nil {
					outputNano = &n
				}
			}
		}
		models = append(models, api.ProviderModelMeta{
			ID:                 m.ID,
			InputPer1KNanoUSD:  inputNano,
			OutputPer1KNanoUSD: outputNano,
			PricedAs:           m.PricedAs,
			ContextLength:      m.ContextLength,
			Capabilities:       modelCapabilitiesToAPI(capabilities),
			Thinking:           thinkingCapabilitiesToDTO(modelcall.ResolveThinkingCapabilities(profile, m, m.ID)),
			Eligibility:        modelRolesToAPI(kind, m, exclusions),
		})
	}
	return models
}

func modelCapabilitiesToAPI(c modelinfo.ModelCapabilities) api.ProviderModelCapabilities {
	project := func(e modelinfo.CapabilityEvidence) api.ProviderCapabilityEvidence {
		state := e.State
		if state == "" {
			state = modelinfo.CapabilityUnknown
		}
		return api.ProviderCapabilityEvidence{State: string(state), Sources: append([]string(nil), e.Sources...)}
	}
	return api.ProviderModelCapabilities{
		Chat: project(c.Chat), Streaming: project(c.Streaming), Tools: project(c.Tools),
		Vision: project(c.Vision), Reasoning: project(c.Reasoning),
		StructuredOutput: project(c.StructuredOutput),
		PromptCaching:    project(c.PromptCaching),
	}
}

func providerFeaturesFromSnapshot(snapshot *providerRegistrySnapshot, id string) api.ProviderFeatures {
	profile := providerprofile.Default()
	if snapshot != nil {
		p := snapshot.providers[id]
		if p != nil {
			profile = p.Profile()
		}
	}
	return api.ProviderFeatures{
		ToolCalls:   profile.ToolCalls.SupportsToolCalls(),
		Thinking:    profile.Thinking != modelinfo.ThinkStyleNone,
		PromptCache: profile.PromptCache.Mode.WireValue(),
	}
}

// List is catalog membership. A missing live provider is not ready.
func (r *Registry) List(ctx context.Context) []api.ProviderMeta {
	out := make([]api.ProviderMeta, 0)
	hydrated := make(map[string][]modelinfo.Entry)
	snapshot := r.snapshot.Load()
	feedDoc, feedStatus, feedUsable := r.feedSnapshot()
	seen := make(map[string]struct{})
	if r.catalog != nil {
		for _, entry := range r.catalog.List() {
			if !PlatformsSupportedHere(entry.Platforms) {
				continue
			}
			seen[entry.ID] = struct{}{}
			meta, models := r.listMetaForEntry(ctx, entry, snapshot, feedDoc, feedStatus, feedUsable)
			hydrated[entry.ID] = models
			out = append(out, meta)
		}
	}
	if snapshot != nil {
		for _, id := range snapshot.ids {
			if _, ok := seen[id]; ok {
				continue
			}
			entry, exists := snapshot.entries[id]
			if !exists {
				if snapshot.providers[id] == nil {
					continue
				}
				entry = CatalogEntry{ID: id}
			}
			if !PlatformsSupportedHere(entry.Platforms) {
				continue
			}
			meta, models := r.listMetaForEntry(ctx, entry, snapshot, feedDoc, feedStatus, feedUsable)
			hydrated[id] = models
			out = append(out, meta)
		}
		r.publishHydratedProviders(snapshot, hydrated)
	}
	return out
}

func (r *Registry) listMetaForEntry(
	ctx context.Context,
	entry CatalogEntry,
	snapshot *providerRegistrySnapshot,
	feedDoc *modelfeed.Document,
	feedStatus string,
	feedUsable bool,
) (api.ProviderMeta, []modelinfo.Entry) {
	if snapshot == nil || snapshot.providers[entry.ID] == nil {
		return r.catalogOnlyMeta(entry, snapshot), entry.Models
	}
	configured := snapshot.configured[entry.ID]
	merged := r.mergeForEntrySnapshotWithFeed(
		ctx, entry, snapshot, feedDoc, feedStatus, feedUsable,
	)
	meta := api.ProviderMeta{
		ID:                   entry.ID,
		Kind:                 entry.Kind,
		Label:                entry.Label,
		BaseURL:              entry.BaseURL,
		EndpointStyle:        string(entry.EndpointStyle.Normalize()),
		Configured:           configured,
		CredentialPresent:    snapshot.credentialStored[entry.ID],
		CredentialSource:     r.credentialSource(entry, configured, snapshot.credentialStored[entry.ID]),
		RequiresAPIKey:       entry.RequiresAPIKey,
		AmbientAuth:          entry.AmbientAuth,
		SecretScreenTrusted:  entry.SecretScreenTrusted(),
		Platforms:            append([]string(nil), entry.Platforms...),
		Features:             providerFeaturesFromSnapshot(snapshot, entry.ID),
		Models:               modelEntriesToAPI(merged.Models, entry.Kind, r.roleExclusions, registryThinkingProfile(snapshot, entry.ID)),
		ConfiguredModels:     modelEntriesToAPI(entry.Models, entry.Kind, r.roleExclusions, registryThinkingProfile(snapshot, entry.ID)),
		ReadyToAssign:        ReadyToAssignExistsSlot(configured, entry.Kind, merged.Models, r.roleExclusions),
		CatalogAuthoritative: merged.CatalogAuthoritative,
		CatalogStatus:        merged.CatalogStatus,
		DiscoveryStatus:      merged.DiscoveryStatus,
	}
	if q := r.providerUsageQuota(ctx, entry); q != nil {
		meta.UsageQuota = q
	}
	if observation, ok := snapshot.observations[entry.ID]; ok && !observation.AcceptedAt.IsZero() {
		verified := observation.AcceptedAt
		meta.CredentialVerifiedAt = &verified
	}
	return meta, merged.Models
}

func (r *Registry) catalogOnlyMeta(entry CatalogEntry, snapshot *providerRegistrySnapshot) api.ProviderMeta {
	stored := false
	if snapshot != nil {
		stored = snapshot.credentialStored[entry.ID]
	}
	meta := api.ProviderMeta{
		ID:                  entry.ID,
		Kind:                entry.Kind,
		Label:               entry.Label,
		BaseURL:             entry.BaseURL,
		EndpointStyle:       string(entry.EndpointStyle.Normalize()),
		Configured:          false,
		CredentialPresent:   stored,
		CredentialSource:    r.credentialSource(entry, false, stored),
		RequiresAPIKey:      entry.RequiresAPIKey,
		AmbientAuth:         entry.AmbientAuth,
		SecretScreenTrusted: entry.SecretScreenTrusted(),
		Platforms:           append([]string(nil), entry.Platforms...),
		Features:            providerFeaturesFromSnapshot(snapshot, entry.ID),
		Models:              modelEntriesToAPI(entry.Models, entry.Kind, r.roleExclusions, registryThinkingProfile(snapshot, entry.ID)),
		ConfiguredModels:    modelEntriesToAPI(entry.Models, entry.Kind, r.roleExclusions, registryThinkingProfile(snapshot, entry.ID)),
		ReadyToAssign:       false,
		DiscoveryStatus:     "unavailable",
	}
	return meta
}

func (r *Registry) credentialSource(entry CatalogEntry, configured, stored bool) string {
	if stored {
		return "stored"
	}
	if configured && !entry.RequiresAPIKey && entry.AmbientAuth != "" {
		return "ambient"
	}
	return "none"
}

// cloneProviderMetaList preserves empty slices for array serialization.
func cloneProviderMetaList(in []api.ProviderMeta) []api.ProviderMeta {
	out := make([]api.ProviderMeta, len(in))
	copy(out, in)
	return out
}

// ListCached returns stale data while one refresh runs.
func (r *Registry) ListCached(ctx context.Context) []api.ProviderMeta {
	out, has, fresh, generation := r.listCache.Read()
	if fresh {
		return out
	}
	if has {
		r.scheduleListCacheRefresh(ctx, generation)
		return out
	}
	data := r.List(ctx)
	r.listCache.Store(data, generation)
	return data
}

func (r *Registry) scheduleListCacheRefresh(ctx context.Context, generation uint64) {
	if !r.listCache.BeginRefresh() {
		return
	}
	// Preserve request values during the detached refresh.
	refreshCtx := context.WithoutCancel(ctx)
	go func() {
		defer r.listCache.EndRefresh()
		r.listCache.Store(r.List(refreshCtx), generation)
	}()
}

// InvalidateListCache advances the cache generation after publication.
func (r *Registry) InvalidateListCache() {
	r.listCache.Invalidate()
}

func (r *Registry) providerUsageQuota(ctx context.Context, entry CatalogEntry) *api.ProviderUsageQuota {
	if entry.Kind != "cloudflare-workers-ai" || !r.IsConfigured(entry.ID) {
		return nil
	}
	accountID, err := openaicompat.CloudflareAccountIDFromBaseURL(entry.BaseURL)
	if err != nil {
		return nil
	}
	apiKey := r.resolveAPIKey(entry)
	if apiKey == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, cloudflareUsageFetchTimeout)
	defer cancel()
	return r.cloudflareUsage.Quota(ctx, r.discoveryClient, accountID, apiKey)
}
