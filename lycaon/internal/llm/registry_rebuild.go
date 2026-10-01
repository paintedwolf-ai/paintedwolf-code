package llm

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
)

// ensureConfigured retries ambient credential resolution.
func (r *Registry) ensureConfigured(ctx context.Context, providerID string) (bool, error) {
	providerID = strings.TrimSpace(providerID)
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return false, &failure.ProviderNotConfiguredError{ProviderID: providerID}
	}
	if snapshot.configured[providerID] {
		return true, nil
	}
	entry, exists := snapshot.entries[providerID]
	if !exists {
		return false, &failure.ProviderNotConfiguredError{ProviderID: providerID}
	}
	if strings.TrimSpace(entry.AmbientAuth) == "" {
		return false, nil
	}

	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	current := r.snapshot.Load()
	if current == nil {
		return false, &failure.ProviderNotConfiguredError{ProviderID: providerID}
	}
	if current.configured[providerID] {
		return true, nil
	}
	entry, exists = current.entries[providerID]
	if !exists {
		return false, &failure.ProviderNotConfiguredError{ProviderID: providerID}
	}
	key := r.resolveAPIKey(entry)
	state, ready := r.entryReadiness(ctx, entry, key)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	provider, buildErr := newProviderForEntry(ctx, entry, key, r.providerRetryPolicy(entry), r.cloudflareUsage, r.discoveryClient)
	if buildErr != nil {
		state = providerReadiness{Configuration: "invalid", Authentication: "not_applicable"}
		ready = false
	}

	providers := current.cloneProviders()
	if provider != nil {
		providers[providerID] = wrapProviderIfDebug(provider)
	}
	credentials := current.cloneCredentialValues()
	credentials[providerID] = key
	stored := current.cloneCredentialStored()
	stored[providerID] = key != ""
	configured := current.cloneConfigured()
	configured[providerID] = ready
	readiness := current.cloneReadiness()
	readiness[providerID] = state
	defaultID := current.defaultID
	if ready && !configured[defaultID] {
		defaultID = providerID
	}
	r.snapshot.Store(newProviderRegistrySnapshot(providerRegistrySnapshotInput{
		Providers: providers, Entries: current.cloneEntries(), CredentialValues: credentials,
		CredentialStored: stored, DefaultID: defaultID, Configured: configured, Readiness: readiness,
		Observations: current.cloneObservations(),
	}))
	r.InvalidateListCache()
	return ready, buildErr
}

func (r *Registry) rebuild(ctx context.Context, invalidateObservations ...string) error {
	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	return r.rebuildLocked(ctx, invalidateObservations...)
}

func (r *Registry) rebuildLocked(ctx context.Context, invalidateObservations ...string) error {
	next := make(map[string]modelcall.Provider)
	entries := make(map[string]CatalogEntry)
	credentialValues := make(map[string]string)
	credentialStored := make(map[string]bool)
	configured := make(map[string]bool)
	readiness := make(map[string]providerReadiness)
	feedDoc, feedStatus, feedUsable := r.feedSnapshot()
	defaultID := ""
	for _, entry := range r.catalog.List() {
		key := r.resolveAPIKey(entry)
		entries[entry.ID] = entry
		credentialValues[entry.ID] = key
		credentialStored[entry.ID] = key != ""
		state, ready := r.entryReadiness(ctx, entry, key)
		configured[entry.ID] = ready
		readiness[entry.ID] = state
		p, err := newProviderForEntry(ctx, entry, key, r.providerRetryPolicy(entry), r.cloudflareUsage, r.discoveryClient)
		if err != nil {
			configured[entry.ID] = false
			readiness[entry.ID] = providerReadiness{
				Configuration:  "invalid",
				Authentication: "not_applicable",
			}
			continue
		}
		if ready {
			effective := r.effectiveModelsForEntryLocked(
				ctx, entry, p, key, ready, feedDoc, feedStatus, feedUsable,
			)
			p = hydrateProviderModels(p, effective)
		}
		next[entry.ID] = wrapProviderIfDebug(p)
		if defaultID == "" && ready {
			defaultID = entry.ID
		}
	}
	if defaultID == "" {
		for _, entry := range r.catalog.List() {
			defaultID = entry.ID
			break
		}
	}
	observations := map[string]providerObservation{}
	if current := r.snapshot.Load(); current != nil {
		observations = current.cloneObservations()
	}
	for _, id := range invalidateObservations {
		delete(observations, strings.TrimSpace(id))
	}
	r.snapshot.Store(newProviderRegistrySnapshot(providerRegistrySnapshotInput{
		Providers: next, Entries: entries, CredentialValues: credentialValues,
		CredentialStored: credentialStored,
		DefaultID:        defaultID, Configured: configured, Readiness: readiness, Observations: observations,
	}))
	// Publish the snapshot before advancing the cache generation.
	r.InvalidateListCache()
	return nil
}

// Reload refreshes catalog and rebuilds providers.
func (r *Registry) Reload(ctx context.Context) error {
	return r.reload(ctx)
}

// ReloadProviderMutation rebuilds and invalidates one authentication observation.
func (r *Registry) ReloadProviderMutation(ctx context.Context, providerID string) error {
	return r.reload(ctx, providerID)
}

func (r *Registry) reload(ctx context.Context, invalidateObservations ...string) error {
	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	if err := r.catalog.Reload(); err != nil {
		return err
	}
	if len(invalidateObservations) == 0 {
		r.discovery.InvalidateAll()
	} else {
		for _, providerID := range invalidateObservations {
			r.discovery.Invalidate(strings.TrimSpace(providerID))
		}
	}
	r.cloudflareUsage.InvalidateAll()
	return r.rebuildLocked(ctx, invalidateObservations...)
}

// Register adds a runtime provider (tests).
func (r *Registry) Register(p modelcall.Provider) error {
	if p == nil || p.ID() == "" {
		return fmt.Errorf("invalid provider")
	}
	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	current := r.snapshot.Load()
	next := map[string]modelcall.Provider{}
	observations := map[string]providerObservation{}
	defaultID := p.ID()
	if current != nil {
		next = current.cloneProviders()
		observations = current.cloneObservations()
		defaultID = current.defaultID
		if defaultID == "" {
			defaultID = p.ID()
		}
	}
	next[p.ID()] = wrapProviderIfDebug(p)
	configured := map[string]bool{p.ID(): true}
	readiness := map[string]providerReadiness{p.ID(): {Configuration: "valid", Authentication: "not_applicable"}}
	if current != nil {
		configured = current.cloneConfigured()
		configured[p.ID()] = true
		readiness = current.cloneReadiness()
		readiness[p.ID()] = providerReadiness{Configuration: "valid", Authentication: "not_applicable"}
	}
	entries := map[string]CatalogEntry{}
	credentialValues := map[string]string{}
	credentialStored := map[string]bool{}
	if current != nil {
		entries = current.cloneEntries()
		credentialValues = current.cloneCredentialValues()
		credentialStored = current.cloneCredentialStored()
	}
	r.snapshot.Store(newProviderRegistrySnapshot(providerRegistrySnapshotInput{
		Providers: next, Entries: entries, CredentialValues: credentialValues,
		CredentialStored: credentialStored,
		DefaultID:        defaultID, Configured: configured, Readiness: readiness, Observations: observations,
	}))
	r.InvalidateListCache()
	return nil
}

func (r *Registry) publishHydratedProviders(
	expected *providerRegistrySnapshot,
	modelsByProvider map[string][]modelinfo.Entry,
) {
	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	current := r.snapshot.Load()
	if current == nil || current != expected {
		return
	}
	next := current.cloneProviders()
	for providerID, models := range modelsByProvider {
		p, ok := current.providers[providerID]
		if !ok {
			continue
		}
		next[providerID] = wrapProviderIfDebug(hydrateProviderModels(p, models))
	}
	r.snapshot.Store(newProviderRegistrySnapshot(providerRegistrySnapshotInput{
		Providers: next, Entries: current.cloneEntries(), CredentialValues: current.cloneCredentialValues(),
		CredentialStored: current.cloneCredentialStored(),
		DefaultID:        current.defaultID, Configured: current.cloneConfigured(), Readiness: current.cloneReadiness(),
		Observations: current.cloneObservations(),
	}))
}

func (r *Registry) publishConnectivityResult(
	expected *providerRegistrySnapshot,
	providerID string,
	models []modelinfo.Entry,
) {
	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	current := r.snapshot.Load()
	if current == nil || current != expected {
		return
	}
	next := current.cloneProviders()
	if p := current.providers[providerID]; p != nil {
		next[providerID] = wrapProviderIfDebug(hydrateProviderModels(p, models))
	}
	observations := current.cloneObservations()
	observations[providerID] = providerObservation{AcceptedAt: time.Now().UTC()}
	r.snapshot.Store(newProviderRegistrySnapshot(providerRegistrySnapshotInput{
		Providers: next, Entries: current.cloneEntries(), CredentialValues: current.cloneCredentialValues(),
		CredentialStored: current.cloneCredentialStored(),
		DefaultID:        current.defaultID, Configured: current.cloneConfigured(), Readiness: current.cloneReadiness(),
		Observations: observations,
	}))
}

func (r *Registry) providerRetryPolicy(entry CatalogEntry) providerretry.ProviderHTTPRetry {
	policy := entry.HTTPRetry.Clone()
	policy = policy.BindRateGate(r.rateGate, entry.ID)
	endpoint, err := url.Parse(entry.BaseURL)
	if err != nil || endpoint.Hostname() == "" {
		return policy
	}
	port := endpoint.Port()
	if port == "" {
		port = "80"
		if strings.EqualFold(endpoint.Scheme, "https") {
			port = "443"
		}
	}
	policy = policy.BindRateGate(r.rateGate, strings.ToLower(endpoint.Scheme+"://"+endpoint.Hostname())+":"+port)
	return policy
}
