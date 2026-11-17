package contract

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/webresearch"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

func TestWebSearchSchemaOmitsProvider(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cfg, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir", err)
	meta, ok := cfg.ToolMeta("web_search")
	if !ok {
		t.Fatal("web_search tool schema missing")
	}
	props, _ := meta.ArgsSchema["properties"].(map[string]any)
	if props == nil {
		t.Fatal("web_search.properties missing")
	}
	if _, hasProvider := props["provider"]; hasProvider {
		t.Fatal("web_search LLM schema must not expose provider — host selects backends from Settings")
	}
	for _, key := range []string{"query", "limit"} {
		if _, ok := props[key]; !ok {
			t.Fatalf("web_search.properties.%s missing", key)
		}
	}
}

func TestWebResearchCatalogKinds(t *testing.T) {
	t.Parallel()
	cat, err := webresearch.LoadCatalog()
	contractcheck.FailErr(t, "LoadCatalog", err)

	for _, entry := range cat.Entries() {
		switch entry.Kind {
		case webresearch.KindKeyed, webresearch.KindKeyedExtra, webresearch.KindKeylessEndpoint, webresearch.KindKeyless:
		default:
			t.Fatalf("provider %q has unknown kind %q", entry.ID, entry.Kind)
		}
		if entry.Label == "" || entry.Hint == "" {
			t.Fatalf("provider %q missing label or hint", entry.ID)
		}
		if entry.ID == "direct" || entry.ID == "docs" {
			t.Fatalf("catalog must not include mode-side id %q", entry.ID)
		}
		if entry.DefaultEnabled && entry.Kind != webresearch.KindKeyless {
			t.Fatalf("provider %q default_enabled on non-keyless kind", entry.ID)
		}
		if entry.Pacing != nil && entry.Kind != webresearch.KindKeyless {
			t.Fatalf("provider %q pacing on non-keyless kind", entry.ID)
		}
		if entry.Kind == webresearch.KindKeyless && entry.DefaultEndpoint == "" {
			t.Fatalf("provider %q keyless missing default_endpoint", entry.ID)
		}
		for _, role := range entry.Roles {
			if role != webresearch.RoleResults && role != webresearch.RoleSeeds {
				t.Fatalf("provider %q unknown role %q", entry.ID, role)
			}
		}
		if len(entry.Roles) == 0 && entry.Roles != nil {
			t.Fatalf("provider %q explicit empty roles forbidden", entry.ID)
		}
	}
}

// TestWebSearchProviderEnumCoversCatalog checks catalog and wire parity.
func TestWebSearchProviderEnumCoversCatalog(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cat, err := webresearch.LoadCatalog()
	contractcheck.FailErr(t, "LoadCatalog", err)
	openAPI, err := wirespec.LoadOpenAPIEnums(root)
	contractcheck.FailErr(t, "load OpenAPI enums", err)
	want := append(cat.IDs(), "direct")
	contractcheck.FailSetEqual(t, "WebSearchProvider OpenAPI enum vs catalog ids + direct", want, openAPI["WebSearchProvider"])
}

func TestWebResearchCatalogRegistrySubset(t *testing.T) {
	cat, err := webresearch.LoadCatalog()
	contractcheck.FailErr(t, "LoadCatalog", err)

	reg := webresearch.NewRegistry(cat)
	contractcheck.FailErr(t, "RegisterCatalogProviders", webresearch.RegisterCatalogProviders(t.Context(), reg))

	for _, id := range cat.IDs() {
		entry, ok := cat.Entry(id)
		if !ok {
			t.Fatalf("catalog entry %q missing", id)
		}
		provider := reg.Get(id)
		if provider == nil {
			t.Fatalf("registry missing catalog provider %q", id)
		}
		if provider.Kind() != entry.Kind {
			t.Fatalf("kind mismatch for %q: registry=%q yaml=%q", id, provider.Kind(), entry.Kind)
		}
	}
}

func TestWebResearchCatalogFixtureRejectsKeylessViolations(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	catPath := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "web-research", "host", "web-research-providers.yaml")
	if _, err := os.Stat(catPath); err != nil {
		t.Fatalf("shipped catalog missing: %v", err)
	}
	if _, err := webresearch.LoadCatalog(); err != nil {
		t.Fatalf("shipped catalog must load: %v", err)
	}

	configtest.Overlay(t, map[config.Rel]string{
		config.WebResearchProviders: `
providers:
  - id: bad
    kind: keyed
    label: Bad
    hint: bad
    test_query: t
    default_enabled: true
`,
	})
	if _, err := webresearch.LoadCatalog(); err == nil {
		t.Fatal("expected catalog validation to reject default_enabled on keyed provider")
	}
}

func TestWebResearchNoProviderIDBehaviorBranches(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cat, err := webresearch.LoadCatalog()
	contractcheck.FailErr(t, "LoadCatalog", err)

	webresearchDir := filepath.Join(root, "lycaon", "internal", "webresearch")
	for _, id := range cat.IDs() {
		pattern := `== "` + id + `"`
		cmd := exec.CommandContext(t.Context(), "rg", "-n", pattern, webresearchDir, "-g", "!*_test.go")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("forbidden provider-id conditional %q in webresearch (behavior belongs in catalog specs + credential presence):\n%s",
				pattern, strings.TrimSpace(string(out)))
		}
		if !isRgNoMatch(err) {
			testutil.FailErr(t, "rg "+pattern, err)
		}
	}
}
