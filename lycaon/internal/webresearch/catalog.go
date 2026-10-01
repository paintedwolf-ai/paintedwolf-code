package webresearch

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/catalogruntime"
)

// ProviderKind classifies a catalog provider's configuration shape.
type ProviderKind string

const (
	KindKeyed           ProviderKind = "keyed"
	KindKeyedExtra      ProviderKind = "keyed_extra"
	KindKeylessEndpoint ProviderKind = "keyless_endpoint"
	KindKeyless         ProviderKind = "keyless"
)

// ProviderRole is a catalog entry participation axis (custom fan-out and/or direct seeds).
type ProviderRole string

const (
	RoleResults ProviderRole = "results"
	RoleSeeds   ProviderRole = "seeds"
)

// PacingSpec configures keyless provider call pacing and quota (YAML SSOT).
type PacingSpec struct {
	MinIntervalMS int    `yaml:"min_interval_ms"`
	DailyCap      int    `yaml:"daily_cap"`
	Cooldown429S  int    `yaml:"cooldown_429_s"`
	SharedKey     string `yaml:"shared_key"`
}

// CatalogExtraField is one provider-specific config slot.
type CatalogExtraField struct {
	Name  string `yaml:"name"`
	Env   string `yaml:"env"`
	Label string `yaml:"label"`
}

// CatalogQueryBackoff optionally overrides the default query backoff floor
// (applied to every REST provider) when an index needs a different min word count.
type CatalogQueryBackoff struct {
	MinWords int `yaml:"min_words"`
}

// CatalogEntry is one row from web-research-providers.yaml.
type CatalogEntry struct {
	ID                     string               `yaml:"id"`
	Kind                   ProviderKind         `yaml:"kind"`
	Label                  string               `yaml:"label"`
	CredentialSlot         string               `yaml:"credential_slot"`
	OptionalCredentialSlot string               `yaml:"optional_credential_slot"`
	APIKeyEnv              string               `yaml:"api_key_env"`
	DefaultEndpoint        string               `yaml:"default_endpoint"`
	AllowPrivateEndpoint   bool                 `yaml:"allow_private_endpoint"`
	ExtraFields            []CatalogExtraField  `yaml:"extra_fields"`
	TestQuery              string               `yaml:"test_query"`
	Hint                   string               `yaml:"hint"`
	QueryBackoff           *CatalogQueryBackoff `yaml:"query_backoff"`
	Roles                  []ProviderRole       `yaml:"roles"`
	DefaultEnabled         bool                 `yaml:"default_enabled"`
	Pacing                 *PacingSpec          `yaml:"pacing"`
	// Family / FamilyParams drive runtime RegisterCatalogProviders (ignored by wire-id codegen).
	Family       string            `yaml:"family"`
	FamilyParams map[string]string `yaml:"family_params"`
}

// Catalog is the bundled custom-provider list (direct search is not included).
type Catalog struct {
	entries *catalogruntime.Catalog[CatalogEntry]
}

type catalogFile struct {
	Providers []CatalogEntry `yaml:"providers"`
}

// LoadCatalog reads and validates web-research-providers.yaml.
func LoadCatalog() (*Catalog, error) {
	data, err := config.Read(config.WebResearchProviders)
	if err != nil {
		return nil, fmt.Errorf("read web research catalog: %w", err)
	}
	var raw catalogFile
	if err := config.DecodeYAML(data, &raw); err != nil {
		return nil, fmt.Errorf("parse web research catalog: %w", err)
	}
	if len(raw.Providers) == 0 {
		return nil, fmt.Errorf("web research catalog: no providers")
	}
	normalized := make([]CatalogEntry, 0, len(raw.Providers))
	for _, entry := range raw.Providers {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			return nil, fmt.Errorf("web research catalog: provider missing id")
		}
		if id == directWireProviderID {
			return nil, fmt.Errorf("web research catalog: %q belongs on the direct mode side, not the catalog", id)
		}
		if err := validateCatalogEntry(id, entry); err != nil {
			return nil, err
		}
		entry.ID = id
		normalized = append(normalized, entry)
	}
	return catalogFromEntries(normalized)
}

func catalogFromEntries(entries []CatalogEntry) (*Catalog, error) {
	items := make([]catalogruntime.Item[CatalogEntry], 0, len(entries))
	for _, entry := range entries {
		items = append(items, catalogruntime.Item[CatalogEntry]{
			ID: entry.ID, Spec: entry,
		})
	}
	assembled, err := catalogruntime.Assemble([]catalogruntime.Layer[CatalogEntry]{
		{Name: "bundled web research catalog", Items: items},
	}, func(existing catalogruntime.Item[CatalogEntry], exists bool, incoming catalogruntime.Item[CatalogEntry]) (catalogruntime.Item[CatalogEntry], error) {
		if !exists {
			return incoming, nil
		}
		return existing, fmt.Errorf("web research catalog: duplicate id %q", incoming.ID)
	})
	if err != nil {
		return nil, err
	}
	return &Catalog{entries: assembled}, nil
}

func validateCatalogEntry(id string, entry CatalogEntry) error {
	switch entry.Kind {
	case KindKeyed, KindKeyedExtra, KindKeylessEndpoint, KindKeyless:
	default:
		return fmt.Errorf("web research catalog: %q has unknown kind %q", id, entry.Kind)
	}
	if entry.QueryBackoff != nil && entry.QueryBackoff.MinWords < 1 {
		return fmt.Errorf("web research catalog: %q query_backoff.min_words must be >= 1", id)
	}
	if entry.DefaultEnabled && entry.Kind != KindKeyless {
		return fmt.Errorf("web research catalog: %q default_enabled is only allowed on keyless providers", id)
	}
	if entry.Pacing != nil && entry.Kind != KindKeyless {
		return fmt.Errorf("web research catalog: %q pacing is only allowed on keyless providers", id)
	}
	if entry.Kind == KindKeyless && strings.TrimSpace(entry.DefaultEndpoint) == "" {
		return fmt.Errorf("web research catalog: %q kind keyless requires default_endpoint", id)
	}
	if entry.AllowPrivateEndpoint && entry.Kind != KindKeylessEndpoint {
		return fmt.Errorf("web research catalog: %q allow_private_endpoint is only allowed on keyless_endpoint providers", id)
	}
	if entry.Roles != nil && len(entry.Roles) == 0 {
		return fmt.Errorf("web research catalog: %q roles must not be empty when present", id)
	}
	for _, role := range entry.Roles {
		switch role {
		case RoleResults, RoleSeeds:
		default:
			return fmt.Errorf("web research catalog: %q has unknown role %q", id, role)
		}
	}
	if err := validateCatalogFamily(id, entry); err != nil {
		return err
	}
	return nil
}

// RolesOrDefault returns roles, defaulting to [results] when absent in YAML.
func (e CatalogEntry) RolesOrDefault() []ProviderRole {
	if len(e.Roles) == 0 {
		return []ProviderRole{RoleResults}
	}
	out := make([]ProviderRole, len(e.Roles))
	copy(out, e.Roles)
	return out
}

// HasRole reports whether the entry participates in the given role axis.
func (e CatalogEntry) HasRole(role ProviderRole) bool {
	for _, r := range e.RolesOrDefault() {
		if r == role {
			return true
		}
	}
	return false
}

// Entry returns one catalog row by wire id.
func (c *Catalog) Entry(id string) (CatalogEntry, bool) {
	if c == nil {
		return CatalogEntry{}, false
	}
	item, ok := c.entries.Get(id)
	return cloneCatalogEntry(item.Spec), ok
}

// Entries returns isolated provider rows in catalog file order.
func (c *Catalog) Entries() []CatalogEntry {
	if c == nil {
		return nil
	}
	items := c.entries.Items()
	out := make([]CatalogEntry, len(items))
	for i, item := range items {
		out[i] = cloneCatalogEntry(item.Spec)
	}
	return out
}

func cloneCatalogEntry(entry CatalogEntry) CatalogEntry {
	entry.ExtraFields = append([]CatalogExtraField(nil), entry.ExtraFields...)
	entry.Roles = append([]ProviderRole(nil), entry.Roles...)
	if entry.QueryBackoff != nil {
		queryBackoff := *entry.QueryBackoff
		entry.QueryBackoff = &queryBackoff
	}
	if entry.Pacing != nil {
		pacing := *entry.Pacing
		entry.Pacing = &pacing
	}
	if entry.FamilyParams != nil {
		params := entry.FamilyParams
		entry.FamilyParams = make(map[string]string, len(params))
		for key, value := range params {
			entry.FamilyParams[key] = value
		}
	}
	return entry
}

// IDs returns provider wire ids in catalog file order.
func (c *Catalog) IDs() []string {
	entries := c.Entries()
	ids := make([]string, len(entries))
	for i, entry := range entries {
		ids[i] = entry.ID
	}
	return ids
}

// CredentialSlots returns every credential slot referenced by the catalog.
func (c *Catalog) CredentialSlots() []string {
	if c == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	add := func(slot string) {
		slot = strings.TrimSpace(slot)
		if slot == "" {
			return
		}
		if _, ok := seen[slot]; ok {
			return
		}
		seen[slot] = struct{}{}
		out = append(out, slot)
	}
	for _, entry := range c.Entries() {
		add(entry.CredentialSlot)
		add(entry.OptionalCredentialSlot)
	}
	return out
}
