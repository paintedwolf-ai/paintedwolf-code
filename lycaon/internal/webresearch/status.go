package webresearch

import wire "github.com/lycaon/lycaon/pkg/api"

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
