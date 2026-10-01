package llm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"gopkg.in/yaml.v3"
)

// stageShipProviders installs an isolated provider catalog for one test.
func stageShipProviders(t *testing.T, shipYAML string) {
	t.Helper()
	configtest.Overlay(t, map[config.Rel]string{config.Providers: shipYAML})
}

func mustTestProviderCatalog(t *testing.T, entries ...CatalogEntry) *ProviderCatalog {
	t.Helper()
	defaultRetry := providerretry.ProviderHTTPRetry{
		MaxRetries:  1,
		MaxWaitMs:   1000,
		BackoffMs:   []int{1},
		Statuses:    []int{429},
		WaitHeaders: []string{"Retry-After"},
	}
	// Each kind keeps the prompt-cache profile it ships with.
	ship, err := LoadProviderConfig()
	if err != nil {
		t.Fatalf("load provider kind catalog: %v", err)
	}
	shipProfile := make(map[string]string, len(ship.Providers))
	for _, p := range ship.Providers {
		shipProfile[p.Kind] = p.PromptCacheProfile
	}
	models := make([]ProviderEntry, 0, len(entries))
	for _, e := range entries {
		retry := e.HTTPRetry
		if retry.IsZero() {
			retry = defaultRetry
		}
		var requiresAPIKey *bool
		if e.RequiresAPIKey {
			requires := true
			requiresAPIKey = &requires
		}
		models = append(models, ProviderEntry{
			ID:                 e.ID,
			Kind:               e.Kind,
			Label:              e.Label,
			BaseURL:            e.BaseURL,
			EndpointStyle:      e.EndpointStyle,
			APIKeyEnv:          e.APIKeyEnv,
			RequiresAPIKey:     requiresAPIKey,
			Models:             e.Models,
			HTTPRetryOverride:  &retry,
			PromptCacheProfile: shipProfile[e.Kind],
		})
	}
	shipData, err := yaml.Marshal(ProviderConfig{
		Providers: models, PromptCacheProfiles: ship.PromptCacheProfiles, ModelPromptCache: ship.ModelPromptCache,
	})
	if err != nil {
		t.Fatalf("marshal provider kinds: %v", err)
	}
	data, err := yaml.Marshal(ProviderConfig{Providers: models})
	if err != nil {
		t.Fatalf("marshal providers: %v", err)
	}
	stageShipProviders(t, string(shipData))
	local := filepath.Join(t.TempDir(), "providers.local.yaml")
	if err := os.WriteFile(local, data, 0o644); err != nil {
		t.Fatalf("write providers.local: %v", err)
	}
	catalog, err := NewProviderCatalogAt(local)
	if err != nil {
		t.Fatalf("NewProviderCatalogAt: %v", err)
	}
	return catalog
}

// writeProvidersLocal installs live provider fixtures.
func writeProvidersLocal(t *testing.T, localPath string, shipYAML []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(localPath), 0o750); err != nil {
		t.Fatalf("mkdir local providers: %v", err)
	}
	if err := os.WriteFile(localPath, shipYAML, 0o644); err != nil {
		t.Fatalf("write providers.local: %v", err)
	}
}

func TestProviderCatalogCreatesPrivateConfigDirectory(t *testing.T) {
	stageShipProviders(t, "providers: []\n")
	configDir := filepath.Join(t.TempDir(), "private")
	_, err := NewProviderCatalogAt(filepath.Join(configDir, "providers.local.yaml"))
	if err != nil {
		t.Fatalf("NewProviderCatalogAt: %v", err)
	}
	info, err := os.Stat(configDir)
	if err != nil {
		t.Fatalf("stat config directory: %v", err)
	}
	if info.Mode().Perm() != privateConfigDirMode {
		t.Fatalf("config directory mode = %o, want %o", info.Mode().Perm(), privateConfigDirMode)
	}
}
