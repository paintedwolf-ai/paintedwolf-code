package webresearch

import (
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCatalogValidationRejectsInvalidRoles(t *testing.T) {
	stageCatalogYAML(t, `
providers:
  - id: bad
    kind: keyed
    label: Bad
    hint: bad
    test_query: t
    roles: [nope]
`)
	_, err := LoadCatalog()
	if err == nil {
		t.Fatal("expected error for unknown role")
	}
}

func TestCatalogValidationRejectsEmptyRoles(t *testing.T) {
	stageCatalogYAML(t, `
providers:
  - id: bad
    kind: keyed
    label: Bad
    hint: bad
    test_query: t
    roles: []
`)
	_, err := LoadCatalog()
	if err == nil {
		t.Fatal("expected error for empty roles")
	}
}

func TestCatalogValidationRejectsDefaultEnabledOnKeyed(t *testing.T) {
	stageCatalogYAML(t, `
providers:
  - id: bad
    kind: keyed
    label: Bad
    hint: bad
    test_query: t
    default_enabled: true
`)
	_, err := LoadCatalog()
	if err == nil {
		t.Fatal("expected error for default_enabled on keyed")
	}
}

func TestCatalogValidationRejectsPacingOnKeyed(t *testing.T) {
	stageCatalogYAML(t, `
providers:
  - id: bad
    kind: keyed
    label: Bad
    hint: bad
    test_query: t
    pacing:
      daily_cap: 1
`)
	_, err := LoadCatalog()
	if err == nil {
		t.Fatal("expected error for pacing on keyed")
	}
}

func TestCatalogValidationRejectsKeylessWithoutEndpoint(t *testing.T) {
	stageCatalogYAML(t, `
providers:
  - id: bad
    kind: keyless
    label: Bad
    hint: bad
    test_query: t
`)
	_, err := LoadCatalog()
	if err == nil {
		t.Fatal("expected error for keyless without default_endpoint")
	}
}

func TestCatalogRolesDefaultToResults(t *testing.T) {
	entry := CatalogEntry{ID: "brave", Kind: KindKeyed}
	if !entry.HasRole(RoleResults) {
		t.Fatal("expected default results role")
	}
	if entry.HasRole(RoleSeeds) {
		t.Fatal("did not expect seeds role by default")
	}
}

func TestCatalogValidationRejectsAllowPrivateOnKeyed(t *testing.T) {
	stageCatalogYAML(t, `
providers:
  - id: bad
    kind: keyed
    label: Bad
    hint: bad
    test_query: t
    allow_private_endpoint: true
`)
	_, err := LoadCatalog()
	if err == nil {
		t.Fatal("expected error for allow_private_endpoint on keyed")
	}
}

func TestMwmblCatalogEntryIsKeyless(t *testing.T) {
	cat, err := LoadCatalog()
	testutil.FailErr(t, "LoadCatalog", err)
	entry, ok := cat.Entry("mwmbl")
	if !ok {
		t.Fatal("missing mwmbl")
	}
	if entry.Kind != KindKeyless {
		t.Fatalf("kind = %q want keyless", entry.Kind)
	}
}

// stageCatalogYAML isolates malformed-catalog tests from bundled provider rows.
func stageCatalogYAML(t *testing.T, body string) {
	t.Helper()
	configtest.Only(t, map[config.Rel]string{config.WebResearchProviders: body})
}
