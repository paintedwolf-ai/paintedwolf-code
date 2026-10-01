package providerprofile

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/pkg/api"
)

// PromptCacheStyle is how a provider keeps a prompt prefix warm.
type PromptCacheStyle string

const (
	// PromptCacheExplicitBreakpoints caches up to host-marked boundaries,
	// each with a lifetime the request sets.
	PromptCacheExplicitBreakpoints PromptCacheStyle = "explicit_breakpoints"
	// PromptCacheAutomaticPrefix caches matching prefixes on its own; the
	// request can only help routing and, for some models, mark a boundary.
	PromptCacheAutomaticPrefix PromptCacheStyle = "automatic_prefix"
	// PromptCacheLocalKV reuses a local runner's KV state while the model
	// stays resident.
	PromptCacheLocalKV PromptCacheStyle = "local_kv"
	// PromptCacheNone keeps nothing between requests.
	PromptCacheNone PromptCacheStyle = "none"
)

// WireValue returns the provider metadata token. Zero maps to none.
func (s PromptCacheStyle) WireValue() string {
	if s == "" {
		return string(PromptCacheNone)
	}
	return string(s)
}

// Caches reports whether a prefix can outlive the request that wrote it.
func (s PromptCacheStyle) Caches() bool {
	switch s {
	case PromptCacheExplicitBreakpoints, PromptCacheAutomaticPrefix, PromptCacheLocalKV:
		return true
	default:
		return false
	}
}

func (s PromptCacheStyle) valid() bool {
	return s == PromptCacheNone || s.Caches()
}

// PromptCacheMarker is the per-part marker an automatic-prefix request puts
// on each host-marked boundary.
type PromptCacheMarker string

const (
	// PromptCacheMarkerNone sends no per-part marker.
	PromptCacheMarkerNone PromptCacheMarker = ""
	// PromptCacheMarkerBreakpoint sends prompt_cache_breakpoint.
	PromptCacheMarkerBreakpoint PromptCacheMarker = "prompt_cache_breakpoint"
	// PromptCacheMarkerCacheControl sends cache_control of type ephemeral.
	PromptCacheMarkerCacheControl PromptCacheMarker = "cache_control"
	// PromptCacheMarkerCatalog sends cache_control when the model catalog
	// reports prompt caching for the route, as a LiteLLM proxy does per
	// model; the proxy translates the marker for its upstream.
	PromptCacheMarkerCatalog PromptCacheMarker = "catalog"
)

func (m PromptCacheMarker) valid() bool {
	switch m {
	case PromptCacheMarkerNone, PromptCacheMarkerBreakpoint, PromptCacheMarkerCacheControl, PromptCacheMarkerCatalog:
		return true
	default:
		return false
	}
}

// PromptCacheResidency names how the host learns whether a local runner
// still holds the model, and with it the prefix.
type PromptCacheResidency string

const (
	// PromptCacheResidencyUnknown has no residency probe.
	PromptCacheResidencyUnknown PromptCacheResidency = ""
	// PromptCacheResidencyOllama reads Ollama's /api/ps.
	PromptCacheResidencyOllama PromptCacheResidency = "ollama"
)

// CacheDuration is a YAML duration such as 5m or 1h.
type CacheDuration time.Duration

// Duration returns the value as a time.Duration.
func (d CacheDuration) Duration() time.Duration { return time.Duration(d) }

// UnmarshalYAML parses a Go duration string.
func (d *CacheDuration) UnmarshalYAML(node *yaml.Node) error {
	var raw string
	if err := node.Decode(&raw); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("duration %q: %w", raw, err)
	}
	*d = CacheDuration(parsed)
	return nil
}

// MarshalYAML writes the Go duration string.
func (d CacheDuration) MarshalYAML() (any, error) {
	return time.Duration(d).String(), nil
}

// PromptCacheLifetime is the lifetime an explicit-breakpoint request sets on
// each boundary. A hit refreshes it. The standing prefix changes rarely, so
// it can take a longer lifetime than history, which grows on every call.
type PromptCacheLifetime struct {
	Standing CacheDuration `yaml:"standing,omitempty"`
	History  CacheDuration `yaml:"history,omitempty"`
}

// For returns the lifetime of one boundary tier.
func (l PromptCacheLifetime) For(tier api.PromptCacheTier) time.Duration {
	if tier == api.PromptCacheTierStanding {
		return l.Standing.Duration()
	}
	return l.History.Duration()
}

// PromptCachePolicy is one provider's prompt-cache behavior: how it caches,
// what a request sends to help it, and how long an idle prefix survives.
type PromptCachePolicy struct {
	// Profile names the prompt_cache_profiles entry the policy came from.
	Profile string           `yaml:"-"`
	Mode    PromptCacheStyle `yaml:"mode"`
	// SessionKey sends the session id as prompt_cache_key.
	SessionKey bool `yaml:"session_key,omitempty"`
	// AffinityHeader carries the session id so requests reach the replica
	// that holds the prefix.
	AffinityHeader string `yaml:"affinity_header,omitempty"`
	// Marker is the per-part boundary marker an automatic-prefix request sends.
	Marker PromptCacheMarker `yaml:"marker,omitempty"`
	// RequestMarker also sends a request-level cache_control.
	RequestMarker bool `yaml:"request_marker,omitempty"`
	// Retention is the prompt_cache_retention value to request.
	Retention string `yaml:"retention,omitempty"`
	// Lifetime is what an explicit-breakpoint request sets per boundary.
	Lifetime PromptCacheLifetime `yaml:"lifetime,omitempty"`
	// ColdAfter is how long an idle prefix probably survives when the
	// request sets no lifetime: the provider's documented or measured
	// figure. Zero means nothing is known, and idleness never marks the
	// prefix cold.
	ColdAfter CacheDuration `yaml:"cold_after,omitempty"`
	// KeepAlive asks a local runner to keep the model resident.
	KeepAlive string `yaml:"keep_alive,omitempty"`
	// Residency is the probe that tells whether a local runner still holds
	// the model.
	Residency PromptCacheResidency `yaml:"residency,omitempty"`
}

// StandingColdAfter is how long the standing prefix survives idle: the
// lifetime the request sets on it, else the declared figure. Zero is unknown.
func (p PromptCachePolicy) StandingColdAfter() time.Duration {
	if p.Mode == PromptCacheExplicitBreakpoints && p.Lifetime.Standing > 0 {
		return p.Lifetime.Standing.Duration()
	}
	return p.ColdAfter.Duration()
}

// Validate checks that each field fits the mode that reads it.
func (p PromptCachePolicy) Validate() error {
	if !p.Mode.valid() {
		return fmt.Errorf("unsupported mode %q", p.Mode)
	}
	if !p.Marker.valid() {
		return fmt.Errorf("unsupported marker %q", p.Marker)
	}
	switch p.Residency {
	case PromptCacheResidencyUnknown, PromptCacheResidencyOllama:
	default:
		return fmt.Errorf("unsupported residency %q", p.Residency)
	}
	if p.ColdAfter < 0 {
		return fmt.Errorf("cold_after must not be negative")
	}
	automatic := p.Mode == PromptCacheAutomaticPrefix
	explicit := p.Mode == PromptCacheExplicitBreakpoints
	local := p.Mode == PromptCacheLocalKV
	if !automatic && (p.SessionKey || strings.TrimSpace(p.AffinityHeader) != "" || p.Marker != PromptCacheMarkerNone || p.RequestMarker || p.Retention != "") {
		return fmt.Errorf("session_key, affinity_header, marker, request_marker, and retention need mode automatic_prefix")
	}
	if p.RequestMarker && p.Marker != PromptCacheMarkerCacheControl {
		return fmt.Errorf("request_marker needs marker cache_control")
	}
	if !local && (p.KeepAlive != "" || p.Residency != PromptCacheResidencyUnknown) {
		return fmt.Errorf("keep_alive and residency need mode local_kv")
	}
	if explicit {
		// A request either names both tiers' lifetimes or neither: a provider
		// that fixes the lifetime itself rejects a request that names one, and
		// cold_after then carries the provider's figure.
		if p.Lifetime != (PromptCacheLifetime{}) {
			for tier, lifetime := range map[string]CacheDuration{"standing": p.Lifetime.Standing, "history": p.Lifetime.History} {
				if !slices.Contains(explicitLifetimes, lifetime.Duration()) {
					return fmt.Errorf("lifetime.%s must be 5m or 1h", tier)
				}
			}
			// A longer-lived entry must precede a shorter one.
			if p.Lifetime.Standing < p.Lifetime.History {
				return fmt.Errorf("lifetime.standing must not be shorter than lifetime.history")
			}
			if p.ColdAfter != 0 {
				return fmt.Errorf("cold_after is the lifetime under mode explicit_breakpoints")
			}
		}
	} else if p.Lifetime != (PromptCacheLifetime{}) {
		return fmt.Errorf("lifetime needs mode explicit_breakpoints")
	}
	if p.Mode == PromptCacheNone && p.ColdAfter != 0 {
		return fmt.Errorf("cold_after needs a caching mode")
	}
	return nil
}

// explicitLifetimes are the breakpoint lifetimes explicit-breakpoint
// transports accept.
var explicitLifetimes = []time.Duration{5 * time.Minute, time.Hour}

// PromptCacheRule refines a profile for the models it matches. Rules are
// ordered and the first one that names the profile and matches the model
// wins; its set fields replace the profile's.
type PromptCacheRule struct {
	// Profiles lists the prompt_cache_profiles names the rule refines.
	Profiles []string `yaml:"profiles"`
	// Match lists case-insensitive substrings of the model id.
	Match []string `yaml:"match,omitempty"`
	// Exact lists case-insensitive whole model ids.
	Exact         []string             `yaml:"exact,omitempty"`
	Mode          *PromptCacheStyle    `yaml:"mode,omitempty"`
	Marker        *PromptCacheMarker   `yaml:"marker,omitempty"`
	RequestMarker *bool                `yaml:"request_marker,omitempty"`
	Retention     *string              `yaml:"retention,omitempty"`
	Lifetime      *PromptCacheLifetime `yaml:"lifetime,omitempty"`
	ColdAfter     *CacheDuration       `yaml:"cold_after,omitempty"`
}

func (r PromptCacheRule) matches(profile, model string) bool {
	if !slices.Contains(r.Profiles, profile) {
		return false
	}
	id := strings.ToLower(strings.TrimSpace(model))
	if id == "" {
		return false
	}
	for _, exact := range r.Exact {
		if id == strings.ToLower(strings.TrimSpace(exact)) {
			return true
		}
	}
	for _, sub := range r.Match {
		if sub = strings.ToLower(strings.TrimSpace(sub)); sub != "" && strings.Contains(id, sub) {
			return true
		}
	}
	return false
}

func (r PromptCacheRule) apply(p PromptCachePolicy) PromptCachePolicy {
	if r.Mode != nil && *r.Mode != p.Mode {
		// A new mode keeps none of the fields the profile set for its old one;
		// the rule's own fields below must fit the new mode.
		p = PromptCachePolicy{Profile: p.Profile, Mode: *r.Mode}
	}
	if r.Marker != nil {
		p.Marker = *r.Marker
	}
	if r.RequestMarker != nil {
		p.RequestMarker = *r.RequestMarker
	}
	if r.Retention != nil {
		p.Retention = *r.Retention
	}
	if r.Lifetime != nil {
		p.Lifetime = *r.Lifetime
	}
	if r.ColdAfter != nil {
		p.ColdAfter = *r.ColdAfter
	}
	return p
}

// ValidatePromptCache checks named profiles and the rules that refine them:
// every rule names known profiles, matches something, and yields a valid
// policy for each profile it names.
func ValidatePromptCache(profiles map[string]PromptCachePolicy, rules []PromptCacheRule) error {
	for name, policy := range profiles {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("prompt_cache_profiles contains an empty name")
		}
		if err := policy.Validate(); err != nil {
			return fmt.Errorf("prompt_cache_profile %q: %w", name, err)
		}
	}
	for i, rule := range rules {
		if len(rule.Profiles) == 0 {
			return fmt.Errorf("model_prompt_cache[%d]: profiles is required", i)
		}
		if len(rule.Match)+len(rule.Exact) == 0 {
			return fmt.Errorf("model_prompt_cache[%d]: match or exact is required", i)
		}
		for _, name := range rule.Profiles {
			base, ok := profiles[name]
			if !ok {
				return fmt.Errorf("model_prompt_cache[%d]: unknown profile %q", i, name)
			}
			if err := rule.apply(base).Validate(); err != nil {
				return fmt.Errorf("model_prompt_cache[%d] on %q: %w", i, name, err)
			}
		}
	}
	return nil
}

var promptCacheRules struct {
	mu    sync.RWMutex
	rules []PromptCacheRule
}

// SetPromptCacheRules installs the ordered model rules.
func SetPromptCacheRules(rules []PromptCacheRule) {
	own := make([]PromptCacheRule, len(rules))
	copy(own, rules)
	promptCacheRules.mu.Lock()
	promptCacheRules.rules = own
	promptCacheRules.mu.Unlock()
}

// ForModel returns the policy one model gets: the first rule that names this
// profile and matches the model refines it.
func (p PromptCachePolicy) ForModel(model string) PromptCachePolicy {
	promptCacheRules.mu.RLock()
	defer promptCacheRules.mu.RUnlock()
	for _, rule := range promptCacheRules.rules {
		if rule.matches(p.Profile, model) {
			return rule.apply(p)
		}
	}
	return p
}
