package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// oMLX is a keyless local provider with typed model discovery.
func TestBundledProvidersIncludeOMLX(t *testing.T) {

	cfg, err := llm.LoadProviderConfig()
	contractcheck.FailErr(t, "llm.LoadProviderConfig failed", err)
	var omlx *llm.ProviderEntry
	for i := range cfg.Providers {
		if cfg.Providers[i].ID == "omlx" {
			omlx = &cfg.Providers[i]
			break
		}
	}
	if omlx == nil {
		t.Fatal("bundled providers missing omlx")
	}
	if omlx.BaseURL != "http://localhost:8000/v1" {
		t.Fatalf("base_url = %q, want http://localhost:8000/v1", omlx.BaseURL)
	}
	if omlx.APIKeyEnv != "" {
		t.Fatalf("api_key_env = %q, want keyless", omlx.APIKeyEnv)
	}
	if len(omlx.Platforms) != 1 || omlx.Platforms[0] != llm.PlatformmacOS {
		t.Fatalf("platforms = %#v, want [macos]", omlx.Platforms)
	}

	t.Setenv("LYCAON_TEST", "1")
	t.Cleanup(llm.SetHostProductPlatformForTest(llm.PlatformmacOS))

	catalog := liveCatalogForShipIDs(t, "omlx")
	registry, err := llm.NewRegistry(catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
	contractcheck.FailErr(t, "llm.NewRegistry failed", err)

	if !registry.IsConfigured("omlx") {
		t.Fatal("expected keyless omlx to be configured without a key")
	}

	var meta *struct {
		toolCalls    bool
		requiresAuth bool
		platforms    []string
	}
	for _, p := range registry.List(t.Context()) {
		if p.ID == "omlx" {
			meta = &struct {
				toolCalls    bool
				requiresAuth bool
				platforms    []string
			}{toolCalls: p.Features.ToolCalls, requiresAuth: p.RequiresAPIKey, platforms: p.Platforms}
			break
		}
	}
	if meta == nil {
		t.Fatal("registry.List missing omlx on macos")
	}
	if meta.requiresAuth {
		t.Fatal("omlx should be keyless (requires_api_key=false)")
	}
	if !meta.toolCalls {
		t.Fatal("omlx should report tool-call capability (OpenAI-compatible driver)")
	}
	if len(meta.platforms) != 1 || meta.platforms[0] != llm.PlatformmacOS {
		t.Fatalf("list platforms = %#v, want [macos]", meta.platforms)
	}

	kinds := catalog.KindTemplates()
	foundKind := false
	for _, k := range kinds {
		if k.Kind == "omlx" {
			foundKind = true
			if len(k.Platforms) != 1 || k.Platforms[0] != llm.PlatformmacOS {
				t.Fatalf("kind platforms = %#v, want [macos]", k.Platforms)
			}
			break
		}
	}
	if !foundKind {
		t.Fatal("KindTemplates missing omlx on macos")
	}

	t.Cleanup(llm.SetHostProductPlatformForTest(llm.PlatformLinux))
	catalogLinux, err := llm.NewProviderCatalogAt(filepath.Join(t.TempDir(), "providers.local.yaml"))
	contractcheck.FailErr(t, "linux catalog", err)
	for _, k := range catalogLinux.KindTemplates() {
		if k.Kind == "omlx" {
			t.Fatal("KindTemplates must hide omlx on linux")
		}
	}
	regLinux, err := llm.NewRegistry(catalogLinux, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
	contractcheck.FailErr(t, "linux registry", err)
	for _, p := range regLinux.List(t.Context()) {
		if p.ID == "omlx" {
			t.Fatal("List must hide omlx on linux")
		}
	}
}
