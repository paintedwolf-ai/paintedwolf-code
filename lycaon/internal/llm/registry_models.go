package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerauth"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/modelfeed"
	"github.com/lycaon/lycaon/internal/pricing"
)

// RefreshDiscovery invalidates one discovered model list.
func (r *Registry) RefreshDiscovery(providerID string) {
	r.discovery.Invalidate(providerID)
	r.cloudflareUsage.InvalidateAll()
	r.InvalidateListCache()
}

// RefreshModels invalidates and reloads one discovered model list.
func (r *Registry) RefreshModels(ctx context.Context, providerID string) error {
	providerID = strings.TrimSpace(providerID)
	configured, err := r.ensureConfigured(ctx, providerID)
	if err != nil {
		return err
	}
	if !configured {
		return &failure.ProviderNotConfiguredError{ProviderID: providerID}
	}
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return fmt.Errorf("provider not found")
	}
	entry, ok := snapshot.entries[providerID]
	if !ok {
		return fmt.Errorf("provider not found")
	}
	r.RefreshDiscovery(providerID)
	profile := providerprofile.Default()
	if p := snapshot.providers[providerID]; p != nil {
		profile = p.Profile()
	}
	discovered, err := r.discoverModelsFresh(ctx, entry.ID, profile, entry.BaseURL, snapshot.credentialValues[providerID])
	if err != nil {
		return err
	}
	feedDoc, feedStatus, feedUsable := r.feedSnapshot()
	merged := mergeAssignableModels(
		entry.Kind, entry.Models, discovered, nil, feedDoc, feedStatus, feedUsable,
	)
	r.publishConnectivityResult(snapshot, providerID, merged.Models)
	return nil
}

// EffectiveModels returns catalog models merged with live discovery when configured.
func (r *Registry) EffectiveModels(ctx context.Context, providerID string) []modelinfo.Entry {
	return r.resolveModels(ctx, providerID).Models
}

func (r *Registry) resolveModels(ctx context.Context, providerID string) mergeResult {
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return mergeResult{DiscoveryStatus: DiscoveryStatusSkipped}
	}
	entry, ok := snapshot.entries[providerID]
	if !ok {
		return mergeResult{DiscoveryStatus: DiscoveryStatusSkipped}
	}
	result := r.mergeForEntrySnapshot(ctx, entry, snapshot)
	r.publishHydratedProviders(snapshot, map[string][]modelinfo.Entry{entry.ID: result.Models})
	return result
}

// modelMetadata reads usable prices and capabilities without network waits.
func (r *Registry) modelMetadata(providerID string) []modelinfo.Entry {
	result, refresh := r.modelSnapshot(context.Background(), providerID)
	refresh()
	return result.Models
}

// DiscoveredRate returns validated live prices for the selected model.
func (r *Registry) DiscoveredRate(providerID, model string) (pricing.Rate, bool) {
	return modelinfo.DiscoveredRateFromEntries(r.modelMetadata(providerID), model)
}

// LocalFree reads the catalog classification for per-token charges.
func (r *Registry) LocalFree(instanceID string) bool {
	if r == nil {
		return false
	}
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return false
	}
	entry, ok := snapshot.entries[instanceID]
	return ok && entry.LocalFree
}

// PricedAsModelID resolves the effective model’s pricing identity.
func (r *Registry) PricedAsModelID(instanceID, model string) string {
	if r == nil {
		return model
	}
	kind, _ := r.ProviderKind(instanceID)
	for _, m := range r.modelMetadata(instanceID) {
		if !modelinfo.EquivalentID(kind, m.ID, model) {
			continue
		}
		if pa := strings.TrimSpace(m.PricedAs); pa != "" {
			return pa
		}
		return model
	}
	return model
}

// ModelExists reports whether modelID is known for providerID, including live discovery.
func (r *Registry) ModelExists(providerID, modelID string) bool {
	if strings.TrimSpace(modelID) == "" {
		return true
	}
	kind, _ := r.ProviderKind(providerID)
	for _, m := range r.modelMetadata(providerID) {
		if modelinfo.EquivalentID(kind, m.ID, modelID) {
			return true
		}
	}
	return false
}

// TestConnectivity validates credentials through model discovery.
func (r *Registry) TestConnectivity(ctx context.Context, providerID string) (modelCount int, err error) {
	providerID = strings.TrimSpace(providerID)
	configured, err := r.ensureConfigured(ctx, providerID)
	if err != nil {
		return 0, err
	}
	if !configured {
		return 0, &failure.ProviderNotConfiguredError{ProviderID: providerID}
	}
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return 0, fmt.Errorf("provider not found")
	}
	entry, ok := snapshot.entries[providerID]
	if !ok {
		return 0, fmt.Errorf("provider not found")
	}
	r.RefreshDiscovery(providerID)
	profile := providerprofile.Default()
	if p := snapshot.providers[providerID]; p != nil {
		profile = p.Profile()
	}
	if profile.Discovery == providerprofile.DiscoveryAzure {
		if probeErr := discovery.ProbeAzureDataPlane(ctx, entry.BaseURL, snapshot.credentialValues[providerID], r.discoveryClient); probeErr != nil {
			return 0, probeErr
		}
	}
	discovered, err := r.discoverModelsFresh(
		ctx,
		entry.ID,
		profile,
		entry.BaseURL,
		snapshot.credentialValues[providerID],
	)
	if err != nil {
		return 0, err
	}
	feedDoc, feedStatus, feedUsable := r.feedSnapshot()
	merged := mergeAssignableModels(
		entry.Kind, entry.Models, discovered, nil, feedDoc, feedStatus, feedUsable,
	)
	r.publishConnectivityResult(snapshot, providerID, merged.Models)
	return len(merged.Models), nil
}

func (r *Registry) effectiveModelsForEntryLocked(
	ctx context.Context,
	entry CatalogEntry,
	p modelcall.Provider,
	apiKey string,
	configured bool,
	feedDoc *modelfeed.Document,
	feedStatus string,
	feedUsable bool,
) []modelinfo.Entry {
	if !configured {
		return nil
	}
	profile := p.Profile()
	discovered, discoverErr := r.discoverModelsFresh(ctx, entry.ID, profile, entry.BaseURL, apiKey)
	merged := mergeAssignableModels(
		entry.Kind, entry.Models, discovered, discoverErr, feedDoc, feedStatus, feedUsable,
	)
	return merged.Models
}

func (r *Registry) mergeForEntrySnapshot(
	ctx context.Context,
	entry CatalogEntry,
	snapshot *providerRegistrySnapshot,
) mergeResult {
	feedDoc, feedStatus, feedUsable := r.feedSnapshot()
	return r.mergeForEntrySnapshotWithFeed(
		ctx, entry, snapshot, feedDoc, feedStatus, feedUsable,
	)
}

func (r *Registry) mergeForEntrySnapshotWithFeed(
	ctx context.Context,
	entry CatalogEntry,
	snapshot *providerRegistrySnapshot,
	feedDoc *modelfeed.Document,
	feedStatus string,
	feedUsable bool,
) mergeResult {
	if snapshot == nil || !snapshot.configured[entry.ID] {
		return mergeResult{
			CatalogAuthoritative: CatalogAuthoritative(entry.Kind, feedUsable),
			CatalogStatus:        feedStatus,
			DiscoveryStatus:      DiscoveryStatusSkipped,
			Models:               nil,
		}
	}
	profile := providerprofile.Default()
	if p := snapshot.providers[entry.ID]; p != nil {
		profile = p.Profile()
	}
	discovered, discoverErr := r.discoverModelsFresh(
		ctx,
		entry.ID,
		profile,
		entry.BaseURL,
		snapshot.credentialValues[entry.ID],
	)
	merged := mergeAssignableModels(
		entry.Kind, entry.Models, discovered, discoverErr, feedDoc, feedStatus, feedUsable,
	)
	return merged
}

func (r *Registry) feedSnapshot() (doc *modelfeed.Document, status string, usable bool) {
	feed := r.modelFeed.Load()
	if feed == nil {
		return nil, modelfeed.StatusUnavailable, false
	}
	return feed.Snapshot()
}

func (r *Registry) entryReadiness(ctx context.Context, entry CatalogEntry, apiKey string) (providerReadiness, bool) {
	if entry.Kind == "bedrock" {
		probeCtx, cancel := context.WithTimeout(ctx, discovery.BedrockTimeout)
		defer cancel()
		configuration, authentication := providerauth.BedrockReadiness(probeCtx, entry.BaseURL, apiKey)
		return providerReadiness{Configuration: configuration, Authentication: authentication},
			configuration == "valid" && authentication != "missing"
	}
	if entry.Kind == "vertex" {
		probeCtx, cancel := context.WithTimeout(ctx, discovery.VertexTimeout)
		defer cancel()
		configuration, authentication := providerauth.VertexReadiness(probeCtx)
		return providerReadiness{Configuration: configuration, Authentication: authentication},
			configuration == "valid" && authentication != "missing"
	}
	state := providerReadiness{Configuration: "valid", Authentication: "not_applicable"}
	if entry.RequiresAPIKey {
		state.Authentication = "missing"
		if strings.TrimSpace(apiKey) != "" {
			state.Authentication = "unverified"
		}
	}
	if entry.Kind == "cloudflare-workers-ai" {
		if _, err := openaicompat.CloudflareAccountIDFromBaseURL(entry.BaseURL); err != nil {
			state.Configuration = "invalid"
		}
	}
	return state, state.Configuration == "valid" && state.Authentication != "missing"
}

// ModelHasVision reports whether providerID/modelID declares the vision modality.
func (r *Registry) ModelHasVision(ctx context.Context, providerID, modelID string) bool {
	if r == nil {
		return false
	}
	providerID = strings.TrimSpace(providerID)
	modelID = strings.TrimSpace(modelID)
	if providerID == "" || modelID == "" {
		return false
	}
	kind, _ := r.ProviderKind(providerID)
	for _, m := range r.resolveSelectedModel(ctx, providerID, modelID).Models {
		if modelinfo.EquivalentID(kind, m.ID, modelID) {
			return modelinfo.Supported(m.EffectiveCapabilities().Vision)
		}
	}
	return false
}
