package llm

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"gopkg.in/yaml.v3"
)

// ProviderConfig is the provider catalog shape.
type ProviderConfig struct {
	Providers []ProviderEntry `yaml:"providers"`
	// HTTPRetryProfiles are named ship-catalog policies. Live instances inherit
	// the resolved policy from their provider kind.
	HTTPRetryProfiles map[string]providerHTTPRetryProfile `yaml:"http_retry_profiles,omitempty"`
	// ModelThinking is the ordered model-family thinking policy.
	ModelThinking []modelinfo.ThinkingRule `yaml:"model_thinking,omitempty"`
	// ModelCutoff is the ordered model-family cutoff policy.
	ModelCutoff []CutoffRule `yaml:"model_cutoff,omitempty"`
	// PromptCacheProfiles are named ship-catalog prompt-cache policies. Live
	// instances inherit the policy of their provider kind.
	PromptCacheProfiles map[string]providerprofile.PromptCachePolicy `yaml:"prompt_cache_profiles,omitempty"`
	// ModelPromptCache is the ordered model-family refinement of those
	// policies.
	ModelPromptCache []providerprofile.PromptCacheRule `yaml:"model_prompt_cache,omitempty"`
	// Capacity is the turn-level hold and provider-slot cooldown.
	Capacity ProviderCapacityConfig `yaml:"provider_capacity,omitempty"`
}

// ProviderEntry describes one configured provider instance.
type ProviderEntry struct {
	ID      string `yaml:"id"`
	Kind    string `yaml:"kind,omitempty"`
	Label   string `yaml:"label,omitempty"`
	BaseURL string `yaml:"base_url"`
	// EndpointStyle controls how BaseURL resolves.
	EndpointStyle EndpointStyle `yaml:"endpoint_style,omitempty"`
	// ReasoningWire selects assistant reasoning replay; omitted inherits the kind.
	ReasoningWire providerprofile.ReasoningWireStyle `yaml:"reasoning_wire,omitempty"`
	APIKeyEnv     string                             `yaml:"api_key_env"`
	// RequiresAPIKey selects stored-key or ambient authentication.
	RequiresAPIKey *bool `yaml:"requires_api_key,omitempty"`
	// AmbientAuth names the available credential chain.
	AmbientAuth string            `yaml:"ambient_auth,omitempty"`
	Models      []modelinfo.Entry `yaml:"models"`
	// LocalFree marks inference without per-token charges.
	LocalFree bool `yaml:"local_free,omitempty"`
	// SecretScreenTrust is the destination identity this instance was trusted
	// with credentials under; it holds only while the identity still matches.
	SecretScreenTrust string `yaml:"secret_screen_trust,omitempty"`
	// SecretScreenTrustOperation is the approval operation that installed the
	// trust; empty when a person set it in Settings.
	SecretScreenTrustOperation string `yaml:"secret_screen_trust_operation,omitempty"`
	// Platforms limits the supported host platforms.
	Platforms []string `yaml:"platforms,omitempty"`
	// HTTPRetryProfile names a ship-catalog retry policy. Local instances omit
	// it and inherit the policy resolved for their provider kind.
	HTTPRetryProfile string `yaml:"http_retry_profile,omitempty"`
	// HTTPRetryOverride is a complete inline local-instance policy.
	HTTPRetryOverride *providerretry.ProviderHTTPRetry `yaml:"http_retry,omitempty"`
	// HTTPRetry is the effective runtime policy after profile or override
	// resolution. It is never serialized.
	HTTPRetry providerretry.ProviderHTTPRetry `yaml:"-"`
	// RejectionReasons maps structured HTTP error codes to notice reasons.
	RejectionReasons []ProviderRejectionRule `yaml:"rejection_reasons,omitempty"`
	// PromptCacheProfile names a ship-catalog prompt-cache policy. Local
	// instances omit it and inherit the policy of their provider kind.
	PromptCacheProfile string `yaml:"prompt_cache_profile,omitempty"`
	// PromptCache is the resolved policy. It is never serialized.
	PromptCache providerprofile.PromptCachePolicy `yaml:"-"`
}

// RequiresKey resolves the instance authentication mode.
func (p ProviderEntry) RequiresKey() bool {
	if p.RequiresAPIKey != nil {
		return *p.RequiresAPIKey
	}
	return p.APIKeyEnv != ""
}

// LoadProviderConfig reads the active provider configuration.
func LoadProviderConfig() (*ProviderConfig, error) {
	data, err := config.Read(config.Providers)
	if err != nil {
		return nil, err
	}
	cfg, err := decodeProviderConfig(data)
	if err != nil {
		return nil, fmt.Errorf("parse provider configuration: %w", err)
	}
	if err := resolveProviderHTTPRetryProfiles(&cfg); err != nil {
		return nil, fmt.Errorf("resolve provider retry profiles: %w", err)
	}
	if err := resolveProviderPromptCacheProfiles(&cfg); err != nil {
		return nil, fmt.Errorf("resolve provider prompt-cache profiles: %w", err)
	}
	return &cfg, nil
}

// resolveProviderPromptCacheProfiles validates the named prompt-cache
// policies and the model rules that refine them, and projects each kind's
// policy onto its entry. A kind that names no profile caches nothing.
func resolveProviderPromptCacheProfiles(cfg *ProviderConfig) error {
	if err := providerprofile.ValidatePromptCache(cfg.PromptCacheProfiles, cfg.ModelPromptCache); err != nil {
		return err
	}
	for i := range cfg.Providers {
		entry := &cfg.Providers[i]
		name := strings.TrimSpace(entry.PromptCacheProfile)
		if name == "" {
			entry.PromptCache = providerprofile.PromptCachePolicy{Mode: providerprofile.PromptCacheNone}
			continue
		}
		policy, ok := cfg.PromptCacheProfiles[name]
		if !ok {
			return fmt.Errorf("provider %q references unknown prompt_cache_profile %q", entry.ID, name)
		}
		policy.Profile = name
		entry.PromptCacheProfile = name
		entry.PromptCache = policy
	}
	return nil
}

func decodeProviderConfig(data []byte) (ProviderConfig, error) {
	var cfg ProviderConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return ProviderConfig{}, err
	}
	for _, provider := range cfg.Providers {
		if err := provider.ReasoningWire.Validate(); err != nil {
			return ProviderConfig{}, fmt.Errorf("provider %q: %w", provider.ID, err)
		}
		if err := validateRejectionRules(provider.RejectionReasons); err != nil {
			return ProviderConfig{}, fmt.Errorf("provider %q: %w", provider.ID, err)
		}
		for _, model := range provider.Models {
			if err := model.Thinking.Validate(); err != nil {
				return ProviderConfig{}, fmt.Errorf("provider %q model %q: %w", provider.ID, model.ID, err)
			}
			if err := model.ReasoningEffortLevels.Validate(); err != nil {
				return ProviderConfig{}, fmt.Errorf("provider %q model %q: %w", provider.ID, model.ID, err)
			}
		}
	}
	return cfg, nil
}
