package webresearch

import (
	"context"
)

type Runtime struct {
	Creds    *CredentialStore
	Config   *ConfigStore
	Catalog  *Catalog
	Registry *Registry
}

// InvalidateProvider clears transient state for a repaired provider.
func (rt Runtime) InvalidateProvider(providerID string) {
	if rt.Registry != nil {
		rt.Registry.ResetProviderRuntime(providerID)
	}
	providerSeedCache.reset()
	searchResultCache.reset()
}

// InvalidateCredential clears providers backed by one credential slot.
func (rt Runtime) InvalidateCredential(slot string) {
	if rt.Catalog == nil {
		return
	}
	for _, entry := range rt.Catalog.Entries() {
		if entry.CredentialSlot == slot || entry.OptionalCredentialSlot == slot {
			rt.InvalidateProvider(entry.ID)
		}
	}
}

// TestProvider runs a canned catalog query against one custom provider or the direct pipeline.
func (rt Runtime) TestProvider(ctx context.Context, providerID string, discoverer DirectDiscoverer) (SearchTestResult, error) {
	if rt.Catalog == nil || rt.Registry == nil {
		return SearchTestResult{}, errRuntimeNotConfigured
	}
	rt.InvalidateProvider(providerID)
	settings := DefaultSettings(rt.Creds, rt.Config, rt.Catalog)
	if isDirectProvider(providerID) {
		result := Search(ctx, SearchOptions{
			Query:           catalogTestQuery(rt.Catalog, providerID),
			Limit:           1,
			Provider:        providerID,
			Settings:        settings,
			Discoverer:      discoverer,
			Registry:        rt.Registry,
			SkipResultCache: true,
		})
		return searchTestFromResult(result), nil
	}
	entry, ok := rt.Catalog.Entry(providerID)
	if !ok {
		return SearchTestResult{}, ErrUnknownProvider{ID: providerID}
	}
	if rt.Registry.Get(providerID) == nil {
		return SearchTestResult{}, ErrUnknownProvider{ID: providerID}
	}
	query := entry.TestQuery
	if query == "" {
		query = "test"
	}
	result := Search(ctx, SearchOptions{
		Query:           query,
		Limit:           1,
		Provider:        providerID,
		Settings:        settings,
		Registry:        rt.Registry,
		SkipResultCache: true,
	})
	return searchTestFromResult(result), nil
}

func catalogTestQuery(cat *Catalog, providerID string) string {
	if cat == nil {
		return "test"
	}
	if entry, ok := cat.Entry(providerID); ok && entry.TestQuery != "" {
		return entry.TestQuery
	}
	return "test"
}

// SearchTestResult is the outcome of POST .../providers/{id}/test.
type SearchTestResult struct {
	OK    bool
	Error string
}

func searchTestFromResult(result WebSearchResult) SearchTestResult {
	if len(result.Results) > 0 {
		return SearchTestResult{OK: true}
	}
	if result.Error != "" {
		return SearchTestResult{OK: false, Error: result.Error}
	}
	if len(result.ProvidersSkipped) > 0 {
		sk := result.ProvidersSkipped[0]
		if sk.Reason != "" {
			return SearchTestResult{OK: false, Error: sk.Reason}
		}
	}
	return SearchTestResult{OK: false, Error: "search returned no results"}
}

var errRuntimeNotConfigured = errRuntime("web research runtime not configured")

type errRuntime string

func (e errRuntime) Error() string { return string(e) }

type ErrUnknownProvider struct {
	ID string
}

func (e ErrUnknownProvider) Error() string {
	return "unknown web research provider: " + e.ID
}
