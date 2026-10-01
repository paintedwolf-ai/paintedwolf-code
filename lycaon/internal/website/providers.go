package website

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/llm"
	"gopkg.in/yaml.v3"
)

const generatedHeader = `# Codegened from paintedwolf-ai/lycaon — do not hand-edit.
# Source: lycaon/config/packs/painted-wolf/platform/host/providers.yaml + data/providers.overlay.yaml
# Regenerate: ./task providers:sync (LYCAON_ROOT checkout must match data/paintedwolf.ref).
#
# Per provider:
#   id            stable slug; becomes the panel id and button target
#   name          label shown on the button and in the steps
#   group         local | cloud | custom — drives the section in docs pickers
#   key_url       provider's API-keys page                       (cloud only)
#   download_url  install page                                   (local only)
#   pull          example "pull a model" command                 (local only)
#   billing       extra step rendered before the key step        (e.g. OpenAI)
#   custom        true marks the OpenAI-compatible catch-all (pinned last)
#   requires_api_key  whether the app shows a key field for this AI provider
#   ambient_auth  credential chain usable instead of a saved key
#                 ("aws-sdk-chain", "google-adc"); empty means key-only
#   platforms     host platforms this kind supports (macos, linux, windows).
#                 Empty or omitted means every host. Projected from the engine.
#
# The auth pair decides which setup steps the docs render, so a page can never
# tell a reader to create a key the app will not ask for:
#   requires_api_key + no ambient_auth  → create a key and paste it
#   requires_api_key + ambient_auth     → either; key optional (Bedrock)
#   no requires_api_key + ambient_auth  → ambient only, no key at all (Vertex)
#   no requires_api_key, no ambient     → keyless local AI provider (Ollama)
#
# Within a group, buttons render alphabetically by name; custom entries are
# always pinned last and stay visible regardless of the filter.
`

// WebsiteProviders is the Hugo data/providers.yaml shape.
type WebsiteProviders struct {
	Groups    []Group           `yaml:"groups"`
	Providers []WebsiteProvider `yaml:"providers"`
}

// Group is a provider-picker section (local vs cloud).
type Group struct {
	ID    string `yaml:"id"`
	Label string `yaml:"label"`
}

// WebsiteProvider is one row in the docs provider picker.
type WebsiteProvider struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Group       string `yaml:"group"`
	KeyURL      string `yaml:"key_url,omitempty"`
	DownloadURL string `yaml:"download_url,omitempty"`
	Pull        string `yaml:"pull,omitempty"`
	Billing     string `yaml:"billing,omitempty"`
	Custom      bool   `yaml:"custom,omitempty"`
	// Auth shape, projected from the engine catalog so the rendered steps match
	// what the app actually asks for. See generatedHeader for the truth table.
	RequiresAPIKey bool   `yaml:"requires_api_key"`
	AmbientAuth    string `yaml:"ambient_auth,omitempty"`
	// Platforms is projected from the engine catalog. Empty means every host.
	Platforms []string `yaml:"platforms,omitempty"`
}

// OverlayProvider holds website-only fields keyed by engine provider id.
type OverlayProvider struct {
	WebsiteID   string `yaml:"website_id,omitempty"`
	Group       string `yaml:"group,omitempty"`
	KeyURL      string `yaml:"key_url,omitempty"`
	DownloadURL string `yaml:"download_url,omitempty"`
	Pull        string `yaml:"pull,omitempty"`
	Billing     string `yaml:"billing,omitempty"`
}

// ProvidersOverlay is the hand-maintained paintedwolf-www overlay.
type ProvidersOverlay struct {
	Groups         []Group                    `yaml:"groups"`
	Providers      map[string]OverlayProvider `yaml:"providers"`
	ExtraProviders []WebsiteProvider          `yaml:"extra_providers"`
}

// RenderProvidersYAML merges the bundled engine catalog with the website overlay.
func RenderProvidersYAML(overlayPath string) ([]byte, error) {
	catalog, err := llm.LoadProviderConfig()
	if err != nil {
		return nil, fmt.Errorf("load catalog: %w", err)
	}
	overlay, err := LoadProvidersOverlay(overlayPath)
	if err != nil {
		return nil, fmt.Errorf("load overlay: %w", err)
	}
	out, err := MergeProviders(catalog, overlay)
	if err != nil {
		return nil, err
	}
	body, err := yaml.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("marshal providers: %w", err)
	}
	return append([]byte(generatedHeader), body...), nil
}

// LoadProvidersOverlay reads data/providers.overlay.yaml.
func LoadProvidersOverlay(path string) (*ProvidersOverlay, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var overlay ProvidersOverlay
	if err := yaml.Unmarshal(data, &overlay); err != nil {
		return nil, err
	}
	if len(overlay.Groups) == 0 {
		return nil, fmt.Errorf("overlay groups must not be empty")
	}
	if overlay.Providers == nil {
		overlay.Providers = map[string]OverlayProvider{}
	}
	return &overlay, nil
}

// MergeProviders combines catalog entries with overlay metadata.
func MergeProviders(catalog *llm.ProviderConfig, overlay *ProvidersOverlay) (*WebsiteProviders, error) {
	if catalog == nil {
		return nil, fmt.Errorf("catalog is nil")
	}
	if overlay == nil {
		return nil, fmt.Errorf("overlay is nil")
	}

	seenOverlay := map[string]struct{}{}
	usedWebsiteIDs := map[string]string{}
	providers := make([]WebsiteProvider, 0, len(catalog.Providers)+len(overlay.ExtraProviders))

	for _, entry := range catalog.Providers {
		meta, ok := overlay.Providers[entry.ID]
		if !ok {
			return nil, fmt.Errorf("overlay missing engine provider %q — add it to data/providers.overlay.yaml", entry.ID)
		}
		seenOverlay[entry.ID] = struct{}{}

		websiteID := entry.ID
		if meta.WebsiteID != "" {
			websiteID = meta.WebsiteID
		}
		if prev, exists := usedWebsiteIDs[websiteID]; exists {
			return nil, fmt.Errorf("duplicate website id %q from engine providers %q and %q", websiteID, prev, entry.ID)
		}
		usedWebsiteIDs[websiteID] = entry.ID

		name := strings.TrimSpace(entry.Label)
		if name == "" {
			name = entry.ID
		}
		group := meta.Group
		if group == "" {
			group = deriveGroup(entry)
		}
		if group != "local" && group != "cloud" && group != "custom" {
			return nil, fmt.Errorf("provider %q: invalid group %q", entry.ID, group)
		}

		// An AI provider the app never asks a key for must not carry an API-keys
		// link: the docs would render "create a key" steps for a credential
		// that does not exist (Vertex is ambient-only; local kinds are
		// keyless). Fail closed rather than publish the wrong instructions.
		if meta.KeyURL != "" && !entry.RequiresKey() {
			return nil, fmt.Errorf("provider %q: overlay sets key_url but the app shows no key field for it (requires_api_key is false) — drop key_url from data/providers.overlay.yaml", entry.ID)
		}

		providers = append(providers, WebsiteProvider{
			ID:             websiteID,
			Name:           name,
			Group:          group,
			KeyURL:         meta.KeyURL,
			DownloadURL:    meta.DownloadURL,
			Pull:           meta.Pull,
			Billing:        meta.Billing,
			RequiresAPIKey: entry.RequiresKey(),
			AmbientAuth:    entry.AmbientAuth,
			Platforms:      append([]string(nil), entry.Platforms...),
		})
	}

	for id := range overlay.Providers {
		if _, ok := seenOverlay[id]; !ok {
			return nil, fmt.Errorf("overlay entry %q has no matching engine provider — remove it or add the provider to lycaon/config/packs/painted-wolf/platform/host/providers.yaml", id)
		}
	}

	for _, extra := range overlay.ExtraProviders {
		if extra.ID == "" {
			return nil, fmt.Errorf("extra_providers entry missing id")
		}
		if prev, exists := usedWebsiteIDs[extra.ID]; exists {
			return nil, fmt.Errorf("extra provider id %q collides with engine provider %q", extra.ID, prev)
		}
		usedWebsiteIDs[extra.ID] = "extra"
		if extra.Group != "local" && extra.Group != "cloud" && extra.Group != "custom" {
			return nil, fmt.Errorf("extra provider %q: invalid group %q", extra.ID, extra.Group)
		}
		providers = append(providers, extra)
	}

	sort.SliceStable(providers, func(i, j int) bool {
		pi, pj := providers[i], providers[j]
		if pi.Custom != pj.Custom {
			return !pi.Custom
		}
		if pi.Group != pj.Group {
			return providerGroupOrder(pi.Group) < providerGroupOrder(pj.Group)
		}
		return strings.ToLower(pi.Name) < strings.ToLower(pj.Name)
	})

	return &WebsiteProviders{
		Groups:    overlay.Groups,
		Providers: providers,
	}, nil
}

func deriveGroup(entry llm.ProviderEntry) string {
	if entry.Kind == "openai-compatible" || entry.ID == "openai-compatible" {
		return "custom"
	}
	if entry.AmbientAuth != "" {
		// Ambient cloud AI providers (Bedrock, Vertex) may carry no key env at all;
		// they are hosted, not local.
		return "cloud"
	}
	if entry.APIKeyEnv == "" {
		return "local"
	}
	lower := strings.ToLower(entry.BaseURL)
	if strings.Contains(lower, "localhost") || strings.Contains(lower, "127.0.0.1") {
		return "local"
	}
	return "cloud"
}

func providerGroupOrder(group string) int {
	switch group {
	case "local":
		return 0
	case "cloud":
		return 1
	case "custom":
		return 2
	default:
		return 3
	}
}
