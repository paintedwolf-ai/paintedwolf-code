package contract

import (
	"net/http"
	"testing"

	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// WebSearchProvider enum parity (Go vs OpenAPI vs TS) is enforced generically by
// TestEnumSyncAutoDiscovered; the catalog is anchored to that enum by
// TestWebSearchProviderEnumCoversCatalog. No hand-maintained id list lives here.

func TestWebResearchProvidersStatusStub(t *testing.T) {
	t.Parallel()
	srv := NewStubServer()
	t.Cleanup(srv.Close)
	client := srv.Client()
	var body api.WebResearchProvidersResponse
	contractcheck.DoJSON(t, client, http.MethodGet, srv.URL+"/v1/web-research/providers", nil, http.StatusOK, &body)
	contractcheck.AssertJSONRoundTrip(t, &body)
	var settings api.WebResearchSettings
	contractcheck.DoJSON(t, client, http.MethodGet, srv.URL+"/v1/settings/web-research", nil, http.StatusOK, &settings)
	contractcheck.AssertJSONRoundTrip(t, &settings)
	cat, err := webresearch.LoadCatalog()
	contractcheck.FailErr(t, "LoadCatalog", err)
	wantEnabled := webresearch.DefaultSettings(nil, nil, cat).EnabledProviders
	if len(settings.EnabledProviders) != len(wantEnabled) {
		t.Fatalf("enabled_providers = %v want %v", settings.EnabledProviders, wantEnabled)
	}
	for i, id := range wantEnabled {
		if settings.EnabledProviders[i] != id {
			t.Fatalf("enabled_providers = %v want %v", settings.EnabledProviders, wantEnabled)
		}
	}
	if settings.EnabledProviders[0] != string(api.WebSearchProviderDirect) {
		t.Fatalf("enabled_providers[0] = %q want direct", settings.EnabledProviders[0])
	}
	if len(body.Providers) != len(cat.Entries()) {
		t.Fatalf("providers = %d want %d (catalog)", len(body.Providers), len(cat.Entries()))
	}
	if body.Direct.Card.ProviderID != string(api.WebSearchProviderDirect) {
		t.Fatalf("direct card provider_id = %q", body.Direct.Card.ProviderID)
	}
}
