package llm

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/catalogruntime"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"gopkg.in/yaml.v3"
)

// CatalogEntry is a live AI provider instance (from providers.local.yaml).
type CatalogEntry struct {
	ID                     string
	Kind                   string
	Label                  string
	BaseURL                string
	EndpointStyle          EndpointStyle
	APIKeyEnv              string
	RequiresAPIKey         bool
	RequiresAPIKeyOverride bool
	// ReasoningWireOverride distinguishes a local choice from the kind default.
	ReasoningWire         providerprofile.ReasoningWireStyle
	ReasoningWireOverride bool
	// AmbientAuth names the inherited credential chain.
	AmbientAuth string
	Models      []modelinfo.Entry
	// LocalFree marks zero-cost local inference.
	LocalFree bool
	// SecretScreenTrust is the destination identity this instance was trusted
	// with credentials under; see SecretScreenTrusted.
	SecretScreenTrust string
	// SecretScreenTrustOperation is the approval operation that installed the
	// trust; empty for a person's Settings choice.
	SecretScreenTrustOperation string
	// Empty Platforms supports every host.
	Platforms []string
	// HTTPRetry controls chat request retries.
	HTTPRetry providerretry.ProviderHTTPRetry
	// HTTPRetryOverride distinguishes a locally authored complete policy from
	// the provider kind's resolved profile.
	HTTPRetryOverride        bool
	RejectionReasons         []ProviderRejectionRule
	RejectionReasonsOverride bool
	// PromptCache is the provider kind's prompt-cache policy.
	PromptCache providerprofile.PromptCachePolicy
}

// SecretDestinationID is the resolved transport identity outbound secret
// decisions bind to. The label is presentation only.
func (e CatalogEntry) SecretDestinationID() string {
	return secretmatch.DestinationKey(e.ID, e.Kind, e.BaseURL, string(e.EndpointStyle.Normalize()), e.AmbientAuth)
}

// SecretScreenTrusted reports whether the stored trust still names this
// instance's resolved destination.
func (e CatalogEntry) SecretScreenTrusted() bool {
	return e.SecretScreenTrust != "" && e.SecretScreenTrust == e.SecretDestinationID()
}

// LocalRequiresAPIKey preserves an explicit authentication choice.
func (e CatalogEntry) LocalRequiresAPIKey() *bool {
	if !e.RequiresAPIKeyOverride {
		return nil
	}
	value := e.RequiresAPIKey
	return &value
}

// LocalRejectionReasons preserves explicit rules without pinning kind defaults.
func (e CatalogEntry) LocalRejectionReasons() []ProviderRejectionRule {
	if !e.RejectionReasonsOverride {
		return nil
	}
	return append([]ProviderRejectionRule(nil), e.RejectionReasons...)
}

// ProviderKindTemplate is a ship-catalog kind for the Add AI provider picker.
type ProviderKindTemplate struct {
	Kind           string
	Label          string
	BaseURL        string
	EndpointStyle  EndpointStyle
	RequiresAPIKey bool
	AmbientAuth    string
	Platforms      []string
}

// ProviderCatalog holds ship kind templates and local live instances.
type ProviderCatalog struct {
	mu         sync.RWMutex
	mutationMu sync.Mutex
	localPath  string
	templates  *catalogruntime.Catalog[ProviderEntry]
	entries    *catalogruntime.Catalog[CatalogEntry]
	capacity   CapacityPolicy
}

// NewProviderCatalogAt loads a catalog from an explicit local path.
func NewProviderCatalogAt(localPath string) (*ProviderCatalog, error) {
	c := &ProviderCatalog{
		localPath: localPath,
	}
	if err := c.Reload(); err != nil {
		return nil, err
	}
	return c, nil
}

// NewProviderCatalog loads the ship kind catalog and local instances.
func NewProviderCatalog() (*ProviderCatalog, error) {
	localPath, err := userProvidersLocalPath()
	if err != nil {
		return nil, err
	}
	return NewProviderCatalogAt(localPath)
}

// Reload reads templates and local instances.
func (c *ProviderCatalog) Reload() error {
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	ship, err := LoadProviderConfig()
	if err != nil {
		return fmt.Errorf("load provider kind catalog: %w", err)
	}
	if err := modelinfo.ValidateThinkingRules(ship.ModelThinking); err != nil {
		return fmt.Errorf("provider kind catalog: %w", err)
	}
	if err := ValidateCutoffRules(ship.ModelCutoff); err != nil {
		return fmt.Errorf("provider kind catalog: %w", err)
	}
	for _, p := range ship.Providers {
		if !p.EndpointStyle.Validate() {
			return fmt.Errorf("provider kind catalog: %q: unsupported endpoint_style %q", p.ID, p.EndpointStyle)
		}
		if p.HTTPRetry.IsZero() {
			return fmt.Errorf("provider kind catalog: %q: http_retry is required", p.ID)
		}
		if err := providerretry.ValidateHTTPRetry(p.HTTPRetry); err != nil {
			return fmt.Errorf("provider kind catalog: %q: %w", p.ID, err)
		}
	}

	templates, err := assembleProviderTemplates(ship.Providers)
	if err != nil {
		return err
	}
	thinkingRules := ship.ModelThinking
	cutoffRules := ship.ModelCutoff

	localData, localErr := os.ReadFile(c.localPath)
	if localErr != nil {
		if !os.IsNotExist(localErr) {
			return localErr
		}
		if err := c.seedEmptyLocal(); err != nil {
			return err
		}
		localData, localErr = os.ReadFile(c.localPath)
		if localErr != nil {
			return localErr
		}
	}

	var local ProviderConfig
	local, err = decodeProviderConfig(localData)
	if err != nil {
		return fmt.Errorf("parse local providers: %w", err)
	}
	if err := modelinfo.ValidateThinkingRules(local.ModelThinking); err != nil {
		return fmt.Errorf("local providers: %w", err)
	}
	if err := ValidateCutoffRules(local.ModelCutoff); err != nil {
		return fmt.Errorf("local providers: %w", err)
	}
	if len(local.PromptCacheProfiles) > 0 {
		return fmt.Errorf("local providers: prompt_cache_profiles belong to the provider kind catalog")
	}
	promptCacheRules := append(append([]providerprofile.PromptCacheRule(nil), local.ModelPromptCache...), ship.ModelPromptCache...)
	if err := providerprofile.ValidatePromptCache(ship.PromptCacheProfiles, promptCacheRules); err != nil {
		return fmt.Errorf("local providers: %w", err)
	}

	shipByKind := preferredProviderTemplates(templates)

	items := make([]catalogruntime.Item[CatalogEntry], 0, len(local.Providers))
	for _, p := range local.Providers {
		entry, err := resolveLocalEntry(p, shipByKind)
		if err != nil {
			return err
		}
		items = append(items, catalogruntime.Item[CatalogEntry]{
			ID: entry.ID, Spec: entry,
		})
	}
	entries, err := catalogruntime.Assemble([]catalogruntime.Layer[CatalogEntry]{
		{Name: "local AI providers", Items: items},
	}, func(existing catalogruntime.Item[CatalogEntry], exists bool, incoming catalogruntime.Item[CatalogEntry]) (catalogruntime.Item[CatalogEntry], error) {
		if !exists {
			return incoming, nil
		}
		return existing, fmt.Errorf("local providers: duplicate id %q", incoming.ID)
	})
	if err != nil {
		return err
	}
	thinkingRules = append(append([]modelinfo.ThinkingRule(nil), local.ModelThinking...), thinkingRules...)
	cutoffRules = append(append([]CutoffRule(nil), local.ModelCutoff...), cutoffRules...)

	modelinfo.SetThinkingRules(thinkingRules)
	SetModelCutoffRules(cutoffRules)
	providerprofile.SetPromptCacheRules(promptCacheRules)

	capacity, err := ship.Capacity.Policy()
	if err != nil {
		return fmt.Errorf("provider kind catalog: %w", err)
	}
	if !local.Capacity.IsZero() {
		capacity, err = local.Capacity.Policy()
		if err != nil {
			return fmt.Errorf("local providers: %w", err)
		}
	}

	c.mu.Lock()
	c.templates = templates
	c.entries = entries
	c.capacity = capacity
	c.mu.Unlock()
	return nil
}

// resolveLocalEntry projects one stored instance onto its kind template.
func resolveLocalEntry(p ProviderEntry, shipByKind map[string]ProviderEntry) (CatalogEntry, error) {
	if !p.EndpointStyle.Validate() {
		return CatalogEntry{}, fmt.Errorf("local providers: %q: unsupported endpoint_style %q", p.ID, p.EndpointStyle)
	}
	profileName := strings.TrimSpace(p.HTTPRetryProfile)
	if profileName != "" && p.HTTPRetryOverride != nil {
		return CatalogEntry{}, fmt.Errorf("local providers: %q declares both http_retry_profile and http_retry", p.ID)
	}
	if p.HTTPRetryOverride != nil {
		p.HTTPRetry = p.HTTPRetryOverride.Clone()
	}
	if err := p.ReasoningWire.Validate(); err != nil {
		return CatalogEntry{}, fmt.Errorf("provider %q: %w", p.ID, err)
	}
	entry := catalogFromEntry(p)
	if ship, ok := shipByKind[entry.Kind]; ok {
		if profileName != "" && profileName != ship.HTTPRetryProfile {
			return CatalogEntry{}, fmt.Errorf(
				"local providers: %q: http_retry_profile %q does not match provider kind %q profile %q",
				p.ID, profileName, entry.Kind, ship.HTTPRetryProfile,
			)
		}
		if p.ReasoningWire == "" {
			entry.ReasoningWire = ship.ReasoningWire
		}
		entry.Platforms = append([]string(nil), ship.Platforms...)
		entry.AmbientAuth = ship.AmbientAuth
		entry.LocalFree = ship.LocalFree
		entry.EndpointStyle = ship.EndpointStyle.Normalize()
		if p.RequiresAPIKey == nil {
			entry.RequiresAPIKey = ship.RequiresKey()
		}
		if p.HTTPRetryOverride == nil && entry.HTTPRetry.IsZero() {
			entry.HTTPRetry = ship.HTTPRetry.Clone()
		}
		if len(p.RejectionReasons) == 0 {
			entry.RejectionReasons = append([]ProviderRejectionRule(nil), ship.RejectionReasons...)
		}
		if name := strings.TrimSpace(p.PromptCacheProfile); name != "" && name != ship.PromptCacheProfile {
			return CatalogEntry{}, fmt.Errorf(
				"local providers: %q: prompt_cache_profile %q does not match provider kind %q profile %q",
				p.ID, name, entry.Kind, ship.PromptCacheProfile,
			)
		}
		entry.PromptCache = ship.PromptCache
	} else if profileName != "" {
		return CatalogEntry{}, fmt.Errorf(
			"local providers: %q: http_retry_profile %q has no provider kind template",
			p.ID, profileName,
		)
	}
	if p.HTTPRetryOverride != nil || !entry.HTTPRetry.IsZero() {
		if err := providerretry.ValidateHTTPRetry(entry.HTTPRetry); err != nil {
			return CatalogEntry{}, fmt.Errorf("local providers: %q: %w", p.ID, err)
		}
	}
	return entry, nil
}

// ResolveEntry projects an instance as Reload would, without storing it.
func (c *ProviderCatalog) ResolveEntry(p ProviderEntry) (CatalogEntry, error) {
	c.mu.RLock()
	shipByKind := map[string]ProviderEntry{}
	if c.templates != nil {
		shipByKind = preferredProviderTemplates(c.templates)
	}
	c.mu.RUnlock()
	return resolveLocalEntry(p, shipByKind)
}

// CapacityPolicy is the schedule Reload resolved.
func (c *ProviderCatalog) CapacityPolicy() CapacityPolicy {
	if c == nil {
		return DefaultCapacityPolicy()
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.capacity
}

func assembleProviderTemplates(providers []ProviderEntry) (*catalogruntime.Catalog[ProviderEntry], error) {
	items := make([]catalogruntime.Item[ProviderEntry], 0, len(providers))
	for _, provider := range providers {
		provider.ID = strings.TrimSpace(provider.ID)
		if provider.ID == "" {
			return nil, fmt.Errorf("provider kind catalog: provider missing id")
		}
		items = append(items, catalogruntime.Item[ProviderEntry]{ID: provider.ID, Spec: provider})
	}
	return catalogruntime.Assemble(
		[]catalogruntime.Layer[ProviderEntry]{{Name: "provider kind catalog", Items: items}},
		func(current catalogruntime.Item[ProviderEntry], exists bool, incoming catalogruntime.Item[ProviderEntry]) (catalogruntime.Item[ProviderEntry], error) {
			if exists {
				return current, fmt.Errorf("duplicate provider id %q", incoming.ID)
			}
			return incoming, nil
		},
	)
}

func preferredProviderTemplates(templates *catalogruntime.Catalog[ProviderEntry]) map[string]ProviderEntry {
	byKind := make(map[string]ProviderEntry)
	for _, item := range templates.Items() {
		provider := item.Spec
		kind := provider.Kind
		if kind == "" {
			kind = provider.ID
		}
		current, exists := byKind[kind]
		if !exists || (current.ID != kind && provider.ID == kind) {
			byKind[kind] = provider
		}
	}
	return byKind
}

func (c *ProviderCatalog) seedEmptyLocal() error {
	if err := os.MkdirAll(filepath.Dir(c.localPath), privateConfigDirMode); err != nil {
		return err
	}
	data, err := yaml.Marshal(ProviderConfig{Providers: []ProviderEntry{}})
	if err != nil {
		return err
	}
	return replacePrivateConfig(c.localPath, data)
}

func catalogFromEntry(p ProviderEntry) CatalogEntry {
	kind := p.Kind
	if kind == "" {
		kind = p.ID
	}
	label := p.Label
	if label == "" {
		label = p.ID
	}
	requires := p.RequiresKey()
	return CatalogEntry{
		ReasoningWire:              p.ReasoningWire,
		ReasoningWireOverride:      p.ReasoningWire != "",
		ID:                         p.ID,
		Kind:                       kind,
		Label:                      label,
		BaseURL:                    p.BaseURL,
		EndpointStyle:              p.EndpointStyle.Normalize(),
		APIKeyEnv:                  p.APIKeyEnv,
		RequiresAPIKey:             requires,
		RequiresAPIKeyOverride:     p.RequiresAPIKey != nil,
		AmbientAuth:                p.AmbientAuth,
		Models:                     modelinfo.CloneEntries(p.Models),
		LocalFree:                  p.LocalFree,
		SecretScreenTrust:          strings.TrimSpace(p.SecretScreenTrust),
		SecretScreenTrustOperation: strings.TrimSpace(p.SecretScreenTrustOperation),
		Platforms:                  append([]string(nil), p.Platforms...),
		HTTPRetry:                  p.HTTPRetry.Clone(),
		HTTPRetryOverride:          p.HTTPRetryOverride != nil,
		RejectionReasons:           append([]ProviderRejectionRule(nil), p.RejectionReasons...),
		RejectionReasonsOverride:   len(p.RejectionReasons) > 0,
		PromptCache:                p.PromptCache,
	}
}

// Get returns one live instance.
func (c *ProviderCatalog) Get(id string) (CatalogEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	item, ok := c.entries.Get(id)
	return cloneCatalogEntry(item.Spec), ok
}

// List returns live instances sorted by id.
func (c *ProviderCatalog) List() []CatalogEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	items := c.entries.Items()
	out := make([]CatalogEntry, 0, len(items))
	for _, item := range items {
		out = append(out, cloneCatalogEntry(item.Spec))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ShipEntryForKind returns the preferred ship kind template (id==kind wins).
func (c *ProviderCatalog) ShipEntryForKind(kind string) (ProviderEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return ProviderEntry{}, false
	}
	provider, found := preferredProviderTemplates(c.templates)[kind]
	return cloneProviderEntry(provider), found
}

// KindTemplates returns one preferred template per kind.
func (c *ProviderCatalog) KindTemplates() []ProviderKindTemplate {
	c.mu.RLock()
	defer c.mu.RUnlock()
	byKind := preferredProviderTemplates(c.templates)
	out := make([]ProviderKindTemplate, 0, len(byKind))
	for kind, p := range byKind {
		if !PlatformsSupportedHere(p.Platforms) {
			continue
		}
		label := p.Label
		if label == "" {
			label = kind
		}
		out = append(out, ProviderKindTemplate{
			Kind:           kind,
			Label:          label,
			BaseURL:        p.BaseURL,
			EndpointStyle:  p.EndpointStyle.Normalize(),
			RequiresAPIKey: p.RequiresKey(),
			AmbientAuth:    p.AmbientAuth,
			Platforms:      append([]string(nil), p.Platforms...),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

func cloneCatalogEntry(entry CatalogEntry) CatalogEntry {
	entry.RejectionReasons = append([]ProviderRejectionRule(nil), entry.RejectionReasons...)
	entry.Models = modelinfo.CloneEntries(entry.Models)
	entry.Platforms = append([]string(nil), entry.Platforms...)
	entry.HTTPRetry = entry.HTTPRetry.Clone()
	return entry
}

func cloneProviderEntry(entry ProviderEntry) ProviderEntry {
	entry.RejectionReasons = append([]ProviderRejectionRule(nil), entry.RejectionReasons...)
	entry.Models = modelinfo.CloneEntries(entry.Models)
	entry.Platforms = append([]string(nil), entry.Platforms...)
	entry.HTTPRetry = entry.HTTPRetry.Clone()
	if entry.HTTPRetryOverride != nil {
		cloned := entry.HTTPRetryOverride.Clone()
		entry.HTTPRetryOverride = &cloned
	}
	if entry.RequiresAPIKey != nil {
		requires := *entry.RequiresAPIKey
		entry.RequiresAPIKey = &requires
	}
	return entry
}

// Remove deletes a live instance from the local file.
func (c *ProviderCatalog) Remove(id string) (bool, error) {
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	data, err := os.ReadFile(c.localPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var cfg ProviderConfig
	cfg, err = decodeProviderConfig(data)
	if err != nil {
		return false, fmt.Errorf("parse local providers: %w", err)
	}
	filtered := cfg.Providers[:0]
	found := false
	for _, p := range cfg.Providers {
		if p.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, p)
	}
	if !found {
		return false, nil
	}
	cfg.Providers = filtered
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return true, err
	}
	return true, replacePrivateConfig(c.localPath, out)
}

// Put stores one complete live instance.
func (c *ProviderCatalog) Put(entry ProviderEntry) error {
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	var cfg ProviderConfig
	if data, err := os.ReadFile(c.localPath); err == nil {
		cfg, err = decodeProviderConfig(data)
		if err != nil {
			return fmt.Errorf("parse local providers: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	existingIndex := -1
	for i := range cfg.Providers {
		if cfg.Providers[i].ID == entry.ID {
			existingKind := providerEntryKind(cfg.Providers[i])
			incomingKind := providerEntryKind(entry)
			if existingKind != incomingKind {
				return fmt.Errorf(
					"provider %q: kind is immutable (%q to %q)",
					entry.ID, existingKind, incomingKind,
				)
			}
			existingIndex = i
			break
		}
	}
	if err := entry.ReasoningWire.Validate(); err != nil {
		return fmt.Errorf("provider %q: %w", entry.ID, err)
	}
	retryOverride := entry.HTTPRetryOverride != nil
	if retryOverride {
		entry.HTTPRetry = entry.HTTPRetryOverride.Clone()
	}
	if !retryOverride {
		kind := entry.Kind
		if kind == "" {
			kind = entry.ID
		}
		if ship, ok := c.ShipEntryForKind(kind); ok {
			entry.HTTPRetry = ship.HTTPRetry.Clone()
		}
	}
	if retryOverride || !entry.HTTPRetry.IsZero() {
		if err := providerretry.ValidateHTTPRetry(entry.HTTPRetry); err != nil {
			return fmt.Errorf("provider %q: %w", entry.ID, err)
		}
	}
	if existingIndex < 0 {
		cfg.Providers = append(cfg.Providers, entry)
	} else {
		cfg.Providers[existingIndex] = entry
	}
	if err := os.MkdirAll(filepath.Dir(c.localPath), privateConfigDirMode); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return replacePrivateConfig(c.localPath, data)
}

func providerEntryKind(entry ProviderEntry) string {
	kind := strings.TrimSpace(entry.Kind)
	if kind == "" {
		kind = strings.TrimSpace(entry.ID)
	}
	return kind
}

func replacePrivateConfig(path string, data []byte) error {
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path),
		Source:   bytes.NewReader(data),
		Mode:     privateConfigFileMode,
		DirMode:  privateConfigDirMode,
	})
	return err
}
