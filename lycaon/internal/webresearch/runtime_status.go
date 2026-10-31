package webresearch

import (
	"context"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/webindex"
	wire "github.com/lycaon/lycaon/pkg/api"
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

// BuildProvidersStatus assembles the settings status payload for GET /v1/web-research/providers.
func BuildProvidersStatus(cat *Catalog, reg *Registry, cfg *ConfigStore, creds *CredentialStore, directReady func() bool) wire.WebResearchProvidersResponse {
	settings := DefaultSettings(creds, cfg, cat)
	entries := cat.Entries()
	providers := make([]wire.WebResearchProviderMeta, 0, len(entries))
	for _, entry := range entries {
		providers = append(providers, providerMeta(entry, reg, settings, creds))
	}
	ready := false
	if directReady != nil {
		ready = directReady()
	}
	return wire.WebResearchProvidersResponse{
		Direct: wire.WebResearchDirectStatus{
			Configured: ready,
			Card:       DirectCardContent(),
		},
		Providers: providers,
	}
}

// BuildWebResearchSettings assembles the singleton preferences payload for GET /v1/settings/web-research.
func BuildWebResearchSettings(cat *Catalog, cfg *ConfigStore, creds *CredentialStore) wire.WebResearchSettings {
	settings := DefaultSettings(creds, cfg, cat)
	warming := true
	guessDomains := true
	if cfg != nil {
		warming = cfg.WarmingEnabled()
		guessDomains = cfg.GuessDomains()
	}
	return wire.WebResearchSettings{
		Warming:          warming,
		GuessDomains:     guessDomains,
		SearchEnabled:    settings.SearchEnabled,
		EnabledProviders: append([]string{}, settings.EnabledProviders...),
	}
}

// BuildProviderMeta returns metadata for a single provider id.
func BuildProviderMeta(cat *Catalog, reg *Registry, cfg *ConfigStore, creds *CredentialStore, id string) (wire.WebResearchProviderMeta, bool) {
	if cat == nil {
		return wire.WebResearchProviderMeta{}, false
	}
	entry, ok := cat.Entry(id)
	if !ok {
		return wire.WebResearchProviderMeta{}, false
	}
	settings := DefaultSettings(creds, cfg, cat)
	return providerMeta(entry, reg, settings, creds), true
}

func providerMeta(entry CatalogEntry, reg *Registry, settings Settings, creds *CredentialStore) wire.WebResearchProviderMeta {
	configured := false
	if reg != nil {
		if p := reg.Get(entry.ID); p != nil {
			configured = p.Configured(settings)
		}
	}
	slot := entry.CredentialSlot
	if slot == "" {
		slot = entry.OptionalCredentialSlot
	}
	credPresent := false
	credentialSource := "none"
	if slot != "" && creds != nil {
		credentialSource = creds.CredentialSource(slot)
		credPresent = credentialSource == "stored" || credentialSource == "environment"
	}
	return wire.WebResearchProviderMeta{
		ID:                   wire.WebSearchProvider(entry.ID),
		Kind:                 wire.WebResearchProviderKind(entry.Kind),
		Label:                entry.Label,
		Roles:                wireProviderRoles(entry),
		DefaultEnabled:       entry.DefaultEnabled,
		Configured:           configured,
		CredentialPresent:   credPresent,
		CredentialSource:     credentialSource,
		CredentialSlot:       slot,
		AllowPrivateEndpoint: entry.AllowPrivateEndpoint,
		Config:               copyConfig(settings.Config[entry.ID]),
	}
}

func wireProviderRoles(entry CatalogEntry) []wire.WebResearchProviderRole {
	roles := entry.RolesOrDefault()
	out := make([]wire.WebResearchProviderRole, len(roles))
	for i, role := range roles {
		out[i] = wire.WebResearchProviderRole(role)
	}
	return out
}

func copyConfig(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		if v != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func ValidProviderID(cat *Catalog, id string) bool {
	if cat == nil {
		return false
	}
	_, ok := cat.Entry(id)
	return ok
}

// sanitizeHits normalizes provider-authored result text (markup, entities,
// invisible runes, whitespace) at the fan-in, so catalog providers inherit it.
// Provider ids are host vocabulary and are left alone.
func sanitizeHits(hits []WebHit) []WebHit {
	if len(hits) == 0 {
		return hits
	}
	out := hits[:0]
	for _, h := range hits {
		clean, ok := sanitizeHit(h)
		if !ok {
			continue
		}
		out = append(out, clean)
	}
	return out
}

// sanitizeHit normalizes one hit's free text and drops it when the URL is not a
// fetchable web address — there is no correct repair for one.
func sanitizeHit(h WebHit) (WebHit, bool) {
	h.Title = webindex.NormalizeWebTextForStorage(h.Title, 0)
	h.Snippet = webindex.NormalizeWebTextForStorage(h.Snippet, 0)
	h.Date = webindex.NormalizeWebTextForStorage(h.Date, 0)
	h.URL = strings.TrimSpace(h.URL)
	if !webAddress(h.URL) {
		return WebHit{}, false
	}
	if h.Title == "" {
		h.Title = h.URL
	}
	return h, true
}

// webAddress reports an absolute http(s) URL with a host and no control or
// invisible runes. Scheme is compared on the parsed value, so casing and
// surrounding space cannot smuggle one past.
func webAddress(raw string) bool {
	if raw == "" || raw != normalizeControlRunes(raw) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return false
	}
	return u.Host != ""
}

// normalizeControlRunes strips invisible-format runes, so a URL compared against
// its own normalization reveals them without altering a clean one.
func normalizeControlRunes(raw string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		switch {
		case r >= 0x200b && r <= 0x200f, // zero-width and bidi marks
			r >= 0x202a && r <= 0x202e, // bidi overrides
			r >= 0x2066 && r <= 0x2069, // bidi isolates
			r == 0xfeff:                // BOM / zero-width no-break
			return -1
		}
		return r
	}, raw)
}
