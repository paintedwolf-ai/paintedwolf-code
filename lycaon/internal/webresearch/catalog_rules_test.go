package webresearch

import (
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
)

func loadCatalogFixture(t *testing.T, entries ...string) (*Catalog, error) {
	t.Helper()
	configtest.Overlay(t, map[config.Rel]string{
		config.WebResearchProviders: "providers:\n" + strings.Join(entries, ""),
	})
	return LoadCatalog()
}

const keyedFixture = `
  - id: fixture
    kind: keyed
    label: Fixture
    hint: fixture hint
    test_query: t
`

const keylessFixture = `
  - id: fixture
    kind: keyless
    label: Fixture
    hint: fixture hint
    test_query: t
    default_endpoint: https://search.invalid
`

// Each entry violates one rule of an otherwise loadable fixture.
func TestLoadCatalogRejectsEachRuleViolation(t *testing.T) {
	for _, control := range []string{keyedFixture, keylessFixture} {
		cat, err := loadCatalogFixture(t, control)
		if err != nil {
			t.Fatalf("control fixture rejected: %v\n%s", err, control)
		}
		if ids := cat.IDs(); len(ids) != 1 || ids[0] != "fixture" {
			t.Fatalf("control ids = %v, want [fixture]", ids)
		}
	}
	for _, tc := range []struct {
		name  string
		entry string
		rule  CatalogRule
	}{
		{"missing id", strings.Replace(keyedFixture, "id: fixture", "id: ' '", 1), RuleMissingID},
		{"direct id", strings.Replace(keyedFixture, "id: fixture", "id: direct", 1), RuleDirectID},
		{"unknown kind", strings.Replace(keyedFixture, "kind: keyed", "kind: telepathy", 1), RuleUnknownKind},
		{"missing label", strings.Replace(keyedFixture, "label: Fixture", "label: ''", 1), RuleMissingLabel},
		{"missing hint", strings.Replace(keyedFixture, "hint: fixture hint", "hint: ''", 1), RuleMissingHint},
		{"query backoff", keyedFixture + "    query_backoff: {min_words: 0}\n", RuleQueryBackoffMinWords},
		{"default enabled keyed", keyedFixture + "    default_enabled: true\n", RuleDefaultEnabledNotKeyless},
		{"pacing keyed", keyedFixture + "    pacing: {}\n", RulePacingNotKeyless},
		{"keyless without endpoint", strings.Replace(keylessFixture, "    default_endpoint: https://search.invalid\n", "", 1), RuleKeylessMissingEndpoint},
		{"private endpoint keyed", keyedFixture + "    allow_private_endpoint: true\n", RulePrivateEndpointNotKeylessEndpoint},
		{"empty roles", keyedFixture + "    roles: []\n", RuleEmptyRoles},
		{"unknown role", keyedFixture + "    roles: [gossip]\n", RuleUnknownRole},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadCatalogFixture(t, tc.entry)
			var rejected *CatalogEntryError
			if !errors.As(err, &rejected) || rejected.Rule != tc.rule {
				t.Fatalf("err = %v, want rule %s", err, tc.rule)
			}
		})
	}
}

// default_enabled is the only difference between a keyed entry that loads and
// one that does not.
func TestLoadCatalogRejectsDefaultEnabledOnlyOnKeyedEntries(t *testing.T) {
	if _, err := loadCatalogFixture(t, keyedFixture); err != nil {
		t.Fatalf("keyed entry without default_enabled rejected: %v", err)
	}
	_, err := loadCatalogFixture(t, keyedFixture+"    default_enabled: true\n")
	var rejected *CatalogEntryError
	if !errors.As(err, &rejected) || rejected.ID != "fixture" || rejected.Rule != RuleDefaultEnabledNotKeyless {
		t.Fatalf("err = %v, want fixture rejected for default_enabled", err)
	}
	if _, err := loadCatalogFixture(t, keylessFixture+"    default_enabled: true\n"); err != nil {
		t.Fatalf("keyless entry with default_enabled rejected: %v", err)
	}
}

func TestShippedCatalogLoads(t *testing.T) {
	cat, err := LoadCatalog()
	if err != nil {
		t.Fatalf("shipped catalog: %v", err)
	}
	if len(cat.IDs()) == 0 {
		t.Fatal("shipped catalog has no providers")
	}
}
