package webresearch

import (
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/decide"
)

// Date-only output preserves the source calendar date.
const webHitDateLayout = "2006-01-02"

// directWireProviderID is the built-in direct-search pipeline's provider id.
// It lives in the enabled set beside catalog providers but has no catalog row.
const directWireProviderID = "direct"

// WebHit is one deduped search result.
type WebHit struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	// Empty Date means the source supplied no publication or modification date.
	Date     string `json:"date,omitempty"`
	Provider string `json:"provider"`
}

// SkippedProvider records a provider that did not run.
type SkippedProvider struct {
	Provider string `json:"provider"`
	Reason   string `json:"reason"`
}

// WebSearchResult is the web_search tool JSON payload.
type WebSearchResult struct {
	OK    bool   `json:"ok"`
	Query string `json:"query"`
	// Period records the time window applied to this search.
	Period           string            `json:"period"`
	Results          []WebHit          `json:"results"`
	ProvidersSkipped []SkippedProvider `json:"providers_skipped"`
	Partial          bool              `json:"partial"`
	Error            string            `json:"error,omitempty"`
	// RepeatSearch marks a whole-search cache hit for the same normalized query.
	RepeatSearch bool `json:"repeat_search,omitempty"`
	// Rerank says what the local engine ranked inside this search; absent when it answered nothing.
	Rerank *decide.RerankReceipt `json:"rerank,omitempty"`
	// Unprobed candidates feed post-search warming outside the wire payload.
	residualURLs []string
	// Direct-pipeline fill counts decide whether the post-search seed runs.
	directStrongHits   int
	directMaxResults   int
	directParticipated bool
}

// Settings separates user-enabled providers from bundled fallback result providers.
type Settings struct {
	SearchEnabled         bool
	Keys                  map[string]string
	Config                map[string]map[string]string
	MaxResultsDefault     int
	PerProviderTimeoutSec int
	EnabledProviders      []string
	SoftProviderIDs       []string
}

// DefaultSettings merges credentials, preferences, and catalog environment values.
// Direct-bundled result providers remain separate from user-selected providers.
func DefaultSettings(creds *CredentialStore, cfg *ConfigStore, cat *Catalog) Settings {
	s := Settings{
		SearchEnabled:         true,
		Keys:                  make(map[string]string),
		Config:                make(map[string]map[string]string),
		MaxResultsDefault:     10,
		PerProviderTimeoutSec: 8,
		EnabledProviders:      []string{directWireProviderID},
	}
	if cfg != nil {
		s.SearchEnabled = cfg.SearchEnabled()
		if providers, explicit := cfg.ProviderSelection(); explicit {
			s.EnabledProviders = providers
		}
	}
	if cat != nil {
		for _, entry := range cat.Entries() {
			s.Keys[entry.ID] = resolveAPIKey(entry, creds)
			s.Config[entry.ID] = resolveProviderConfig(entry, cfg)
		}
		expandDirectBundledResults(&s, cat)
	}
	return s
}

// expandDirectBundledResults adds bundled fallback providers when direct search is enabled.
// Seed providers feed crawl channels separately.
func expandDirectBundledResults(s *Settings, cat *Catalog) {
	if s == nil || cat == nil {
		return
	}
	if !containsProviderID(s.EnabledProviders, directWireProviderID) {
		s.SoftProviderIDs = nil
		return
	}

	softSet := make(map[string]struct{})
	soft := make([]string, 0)
	for _, entry := range cat.Entries() {
		if entry.Kind != KindKeyless || !entry.DefaultEnabled || entry.HasRole(RoleSeeds) {
			continue
		}
		if _, seen := softSet[entry.ID]; seen {
			continue
		}
		softSet[entry.ID] = struct{}{}
		soft = append(soft, entry.ID)
	}
	s.SoftProviderIDs = soft
}

func softProviderSet(ids []string) map[string]struct{} {
	if len(ids) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out
}

func containsProviderID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func resolveAPIKey(entry CatalogEntry, creds *CredentialStore) string {
	slot := entry.CredentialSlot
	if slot == "" {
		slot = entry.OptionalCredentialSlot
	}
	if creds != nil && slot != "" {
		if key, ok := creds.Get(slot); ok && strings.TrimSpace(key.Value()) != "" {
			return key.Value()
		}
	}
	if entry.APIKeyEnv != "" {
		return os.Getenv(entry.APIKeyEnv)
	}
	return ""
}

func resolveProviderConfig(entry CatalogEntry, cfg *ConfigStore) map[string]string {
	out := make(map[string]string)
	if cfg != nil {
		for k, v := range cfg.ProviderConfig(entry.ID) {
			out[k] = v
		}
	}
	for _, field := range entry.ExtraFields {
		if strings.TrimSpace(out[field.Name]) != "" {
			continue
		}
		if field.Env != "" {
			if v := os.Getenv(field.Env); strings.TrimSpace(v) != "" {
				out[field.Name] = v
			}
		}
	}
	if entry.Kind == KindKeylessEndpoint || entry.Kind == KindKeyless {
		if strings.TrimSpace(out["endpoint"]) == "" && strings.TrimSpace(entry.DefaultEndpoint) != "" {
			out["endpoint"] = entry.DefaultEndpoint
		}
	}
	return out
}

func providerConfigured(id string, kind ProviderKind, s Settings) bool {
	key := strings.TrimSpace(s.Keys[id])
	switch kind {
	case KindKeyed:
		return key != ""
	case KindKeyedExtra:
		if key == "" {
			return false
		}
		cfg := s.Config[id]
		for _, field := range requiredExtraFields(id) {
			if strings.TrimSpace(cfg[field]) == "" {
				return false
			}
		}
		return true
	case KindKeylessEndpoint:
		endpoint := strings.TrimSpace(s.Config[id]["endpoint"])
		return endpoint != "" || key != ""
	case KindKeyless:
		return true
	default:
		return false
	}
}

func requiredExtraFields(id string) []string {
	switch id {
	case "google_cse":
		return []string{"search_engine_id"}
	default:
		return nil
	}
}

// FilterKnownProviderIDs excludes bundled providers from persisted user selections.
func FilterKnownProviderIDs(cat *Catalog, ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		if !isDirectProvider(id) {
			if cat == nil {
				continue
			}
			entry, ok := cat.Entry(id)
			if !ok {
				continue
			}
			if entry.Kind == KindKeyless && entry.DefaultEnabled {
				continue
			}
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
