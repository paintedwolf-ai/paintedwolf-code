package webresearch

import (
	"net/url"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStackExchangeFamilyNeedsOnlyCatalogParams(t *testing.T) {
	// A family-backed site requires only a catalog row.
	entry := CatalogEntry{
		ID:     "stackexchange_softwareengineering",
		Kind:   KindKeyless,
		Family: FamilyStackExchange,
		FamilyParams: map[string]string{
			"site": "softwareengineering",
		},
		DefaultEndpoint: "https://api.stackexchange.com",
	}
	if err := validateCatalogFamily(entry.ID, entry); err != nil {
		testutil.FailErr(t, "validateCatalogFamily failed", err)
	}
	spec := stackexchangeRESTSpec(entry.ID, entry.FamilyParams["site"])
	if spec.ProviderID != entry.ID {
		t.Fatalf("provider id = %q", spec.ProviderID)
	}
	cat, err := catalogFromEntries([]CatalogEntry{entry})
	testutil.FailErr(t, "catalogFromEntries failed", err)
	r := NewRegistry(cat)
	if err := r.registerCatalogEntry(entry); err != nil {
		testutil.FailErr(t, "r.registerCatalogEntry failed", err)
	}
	if !r.Has(entry.ID) {
		t.Fatalf("expected %q registered from family params alone", entry.ID)
	}
}

func TestPackageRegistryFamilyFromShape(t *testing.T) {
	entry := CatalogEntry{
		ID:     "npm",
		Kind:   KindKeyless,
		Family: FamilyPackageRegistry,
		FamilyParams: map[string]string{
			"shape": "npm",
		},
		DefaultEndpoint: "https://registry.npmjs.org",
	}
	spec, err := packageRegistryRESTSpec(entry)
	testutil.FailErr(t, "packageRegistryRESTSpec failed", err)
	if spec.ProviderID != "npm" {
		t.Fatalf("spec = %+v", spec)
	}
	rawURL, err := spec.BuildURL(Settings{Config: map[string]map[string]string{
		entry.ID: {"endpoint": entry.DefaultEndpoint},
	}}, "example package", 100)
	testutil.FailErr(t, "build package search URL", err)
	u, err := url.Parse(rawURL)
	testutil.FailErr(t, "parse package search URL", err)
	if u.Query().Get("size") != "25" || u.Query().Get("text") != "example package" {
		t.Fatalf("search query = %q", u.RawQuery)
	}
}

func TestUnknownFamilyRejected(t *testing.T) {
	err := validateCatalogFamily("x", CatalogEntry{Family: "not_a_family"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRegisterCatalogProvidersErrorsWithoutBuilder(t *testing.T) {
	entry := CatalogEntry{ID: "acme_search", Kind: KindKeyless, Family: FamilyCustom}
	cat, err := catalogFromEntries([]CatalogEntry{entry})
	testutil.FailErr(t, "catalogFromEntries failed", err)
	err = RegisterCatalogProviders(NewRegistry(cat))
	if err == nil {
		t.Fatal("expected an error for a custom row with no builder")
	}
	if !strings.Contains(err.Error(), entry.ID) {
		t.Fatalf("error must name the provider: %v", err)
	}
}
