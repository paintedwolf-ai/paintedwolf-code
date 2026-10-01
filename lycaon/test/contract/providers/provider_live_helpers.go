package contract

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// writeLiveProvidersLocal seeds providers.local.yaml with the given live
// instances. registry.List and IsConfigured read local rows only; ship
// templates feed the Add picker.
func writeLiveProvidersLocal(t *testing.T, path string, entries ...llm.ProviderEntry) {
	t.Helper()
	data, err := yaml.Marshal(llm.ProviderConfig{Providers: append([]llm.ProviderEntry(nil), entries...)})
	contractcheck.FailErr(t, "marshal providers.local", err)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		contractcheck.FailErr(t, "mkdir providers.local parent", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		contractcheck.FailErr(t, "write providers.local", err)
	}
}

// liveCatalogForShipIDs builds a catalog with only the named ship entries as live
// local instances (first-run empty seed + Add).
func liveCatalogForShipIDs(t *testing.T, ids ...string) *llm.ProviderCatalog {
	t.Helper()
	cfg, err := llm.LoadProviderConfig()
	contractcheck.FailErr(t, "llm.LoadProviderConfig failed", err)
	byID := make(map[string]llm.ProviderEntry, len(cfg.Providers))
	for _, p := range cfg.Providers {
		byID[p.ID] = p
	}
	entries := make([]llm.ProviderEntry, 0, len(ids))
	for _, id := range ids {
		e, ok := byID[id]
		if !ok {
			t.Fatalf("bundled providers missing %q", id)
		}
		entries = append(entries, e)
	}
	local := filepath.Join(t.TempDir(), "providers.local.yaml")
	writeLiveProvidersLocal(t, local, entries...)
	catalog, err := llm.NewProviderCatalogAt(local)
	contractcheck.FailErr(t, "llm.NewProviderCatalogAt failed", err)
	return catalog
}
