package llm

import (
	"context"
	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providers/nativegen"
	"gopkg.in/yaml.v3"
	"os"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBundledCatalogIsHostDefinedOnBothPlatforms(t *testing.T) {
	for _, platform := range []string{PlatformmacOS, PlatformLinux} {
		t.Run(platform, func(t *testing.T) {
			t.Setenv("LYCAON_TEST", "1")
			defer SetHostProductPlatformForTest(platform)()
			t.Setenv(configdir.EnvConfigDir, t.TempDir())
			configtest.Overlay(t, map[config.Rel]string{config.ModelPolicy: emptyModelPolicyYAML})
			catalog, err := NewProviderCatalog()
			testutil.FailErr(t, "load host catalog", err)
			entry, ok := catalog.Get(IncludedProviderID)
			if !ok || entry.Kind != IncludedProviderID || entry.RequiresAPIKey || !entry.LocalFree || len(entry.Models) != 0 {
				t.Fatalf("incorrect bundled transport: %+v", entry)
			}
			data, err := os.ReadFile(catalog.localPath)
			testutil.FailErr(t, "read device catalog", err)
			var stored struct {
				Providers []ProviderEntry `yaml:"providers"`
			}
			testutil.FailErr(t, "decode device catalog", yaml.Unmarshal(data, &stored))
			if len(stored.Providers) != 0 {
				t.Fatal("bundled catalog persisted a device provider")
			}
			testutil.FailErr(t, "reload host catalog", catalog.Reload())
			if _, ok := catalog.Get(IncludedProviderID); !ok {
				t.Fatal("reload lost bundled transport")
			}
		})
	}
}

func TestScopedRoutersObserveCurrentLocalAvailability(t *testing.T) {
	var available atomic.Bool
	remote := ModelRef{ProviderID: "remote", Model: "selected"}
	router := NewStaticModelRouter(NewInMemoryPolicyStore(ModelPolicy{Coordinator: remote})).WithLocalDefault(available.Load)
	scoped := router.WithScope(SettingsScopeProject, t.TempDir())
	for _, present := range []bool{false, true, false} {
		available.Store(present)
		for _, view := range []*StaticModelRouter{router, scoped} {
			coordinator, err := view.Coordinator(t.Context())
			testutil.FailErr(t, "resolve explicit coordinator", err)
			if coordinator == nil || coordinator.ProviderID != remote.ProviderID {
				t.Fatal("local availability changed an explicit selection")
			}
			lite, err := view.Lite(t.Context())
			testutil.FailErr(t, "resolve default lite model", err)
			want := remote.ProviderID
			if present {
				want = IncludedProviderID
			}
			if lite == nil || lite.ProviderID != want {
				t.Fatalf("scoped router retained stale availability: %+v", lite)
			}
		}
	}
}

func publishIncludedModelFixture(t *testing.T, registry *Registry, provider modelcall.Provider) {
	t.Helper()
	current := registry.snapshot.Load()
	entries := current.cloneEntries()
	entries[IncludedProviderID] = CatalogEntry{ID: IncludedProviderID, Kind: IncludedProviderID}
	registry.snapshot.Store(newProviderRegistrySnapshot(providerRegistrySnapshotInput{
		Providers: current.cloneProviders(), Entries: entries,
		Configured: current.cloneConfigured(), Readiness: current.cloneReadiness(),
		DefaultID: current.defaultID,
	}))
	registry.discovery = discovery.NewCache(nil)
	t.Cleanup(func() { _ = registry.discovery.Close(context.Background()) })
	entry := nativegen.DefaultModelEntry()
	entry.ContextLength = 16384
	registry.discovery.Put(discoveryCacheKey(IncludedProviderID, provider.Profile(), "", ""), []modelinfo.Entry{entry}, nil)
}
