package hostcontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/webresearch"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestGetWebResearchProvidersStatus(t *testing.T) {
	reg := webresearch.NewRegistry(contractfixture.MustCatalog(t))
	testutil.FailErr(t, "RegisterCatalogProviders", webresearch.RegisterCatalogProviders(t.Context(), reg))
	cfg := webresearch.NewConfigStoreAt(filepath.Join(t.TempDir(), "web-research-config.yaml"))
	creds := webresearch.NewCredentialStoreAt(filepath.Join(t.TempDir(), "credential-vault.age"), reg.Catalog())
	s := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: sessionstore.NewMemory()}, External: hostapi.ExternalDependencies{WebResearch: webresearch.Runtime{
		Catalog:  reg.Catalog(),
		Registry: reg,
		Config:   cfg,
		Creds:    creds,
	}}}), nil, "test-token")

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/web-research/providers", nil)
	testutil.FailErr(t, "NewRequest", err)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	rawBody := rec.Body.Bytes()
	var body wire.WebResearchProvidersResponse
	testutil.FailErr(t, "Decode", json.Unmarshal(rawBody, &body))
	wantProviders := 0
	for _, id := range reg.Catalog().IDs() {
		if id == string(wire.WebSearchProviderDirect) {
			continue
		}
		wantProviders++
	}
	if len(body.Providers) != wantProviders {
		t.Fatalf("providers = %d want %d", len(body.Providers), wantProviders)
	}
	for _, p := range body.Providers {
		if p.ID == wire.WebSearchProviderDirect {
			t.Fatalf("unexpected direct row in providers: %+v", p)
		}
	}
	if body.Direct.Card.ProviderID != string(wire.WebSearchProviderDirect) {
		t.Fatalf("direct card = %+v", body.Direct.Card)
	}

	settingsReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/settings/web-research", nil)
	testutil.FailErr(t, "NewRequest settings", err)
	settingsReq.Header.Set("Authorization", "Bearer test-token")
	settingsRec := httptest.NewRecorder()
	s.ServeHTTP(settingsRec, settingsReq)
	if settingsRec.Code != http.StatusOK {
		t.Fatalf("settings status = %d body = %s", settingsRec.Code, settingsRec.Body.String())
	}
	var settings wire.WebResearchSettings
	testutil.FailErr(t, "Decode settings", json.Unmarshal(settingsRec.Body.Bytes(), &settings))
	if !settings.SearchEnabled {
		t.Fatal("search_enabled should default true")
	}
	// Direct on by default; bundled results-only keyless rows ride SoftProviderIDs.
	wantEnabled := webresearch.DefaultSettings(creds, cfg, reg.Catalog()).EnabledProviders
	if len(settings.EnabledProviders) != len(wantEnabled) {
		t.Fatalf("enabled_providers = %v want %v out of the box", settings.EnabledProviders, wantEnabled)
	}
	for i, id := range wantEnabled {
		if settings.EnabledProviders[i] != id {
			t.Fatalf("enabled_providers = %v want %v out of the box", settings.EnabledProviders, wantEnabled)
		}
	}
	if settings.EnabledProviders[0] != string(wire.WebSearchProviderDirect) {
		t.Fatalf("enabled_providers[0] = %q want direct", settings.EnabledProviders[0])
	}
	var rawSettings map[string]json.RawMessage
	testutil.FailErr(t, "Decode raw", json.Unmarshal(settingsRec.Body.Bytes(), &rawSettings))
	if string(rawSettings["enabled_providers"]) == "null" {
		t.Fatalf("prefs array must not be JSON null: enabled=%s", rawSettings["enabled_providers"])
	}
}

func TestTestWebResearchProviderUnknown(t *testing.T) {
	reg := webresearch.NewRegistry(contractfixture.MustCatalog(t))
	testutil.FailErr(t, "RegisterCatalogProviders", webresearch.RegisterCatalogProviders(t.Context(), reg))
	s := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: sessionstore.NewMemory()}, External: hostapi.ExternalDependencies{WebResearch: webresearch.Runtime{Catalog: reg.Catalog(), Registry: reg}}}), nil, "test-token")

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/web-research/providers/unknown/test", nil)
	testutil.FailErr(t, "NewRequest", err)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestTestWebResearchProviderNotConfigured(t *testing.T) {
	reg := webresearch.NewRegistry(contractfixture.MustCatalog(t))
	testutil.FailErr(t, "RegisterCatalogProviders", webresearch.RegisterCatalogProviders(t.Context(), reg))
	s := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: sessionstore.NewMemory()}, External: hostapi.ExternalDependencies{WebResearch: webresearch.Runtime{Catalog: reg.Catalog(), Registry: reg}}}), nil, "test-token")

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/web-research/providers/serper/test", nil)
	testutil.FailErr(t, "NewRequest", err)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var body wire.ProviderProbeResult
	testutil.FailErr(t, "Decode", json.NewDecoder(rec.Body).Decode(&body))
	if body.OK {
		t.Fatalf("body = %+v", body)
	}
	if body.Message != "The provider could not complete a search. Check its configuration and credentials." {
		t.Fatalf("probe must return host-owned diagnostic copy: %+v", body)
	}
}

func TestUpdateWebResearchProviderConfigRejectsMetadataEndpoint(t *testing.T) {
	reg := webresearch.NewRegistry(contractfixture.MustCatalog(t))
	testutil.FailErr(t, "RegisterCatalogProviders", webresearch.RegisterCatalogProviders(t.Context(), reg))
	cfg := webresearch.NewConfigStoreAt(filepath.Join(t.TempDir(), "web-research-config.yaml"))
	creds := webresearch.NewCredentialStoreAt(filepath.Join(t.TempDir(), "credential-vault.age"), reg.Catalog())
	s := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: sessionstore.NewMemory()}, External: hostapi.ExternalDependencies{WebResearch: webresearch.Runtime{
		Catalog:  reg.Catalog(),
		Registry: reg,
		Config:   cfg,
		Creds:    creds,
	}}}), nil, "test-token")

	body := `{"config":{"endpoint":"http://169.254.169.254/"}}`
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPatch, "/v1/web-research/providers/searxng", strings.NewReader(body))
	testutil.FailErr(t, "NewRequest", err)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}

	okBody := `{"config":{"endpoint":"http://192.168.1.10/searxng"}}`
	req, err = http.NewRequestWithContext(t.Context(), http.MethodPatch, "/v1/web-research/providers/searxng", strings.NewReader(okBody))
	testutil.FailErr(t, "NewRequest ok", err)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("lan searx status = %d body = %s", rec.Code, rec.Body.String())
	}

	badBrave := `{"config":{"endpoint":"http://192.168.1.10/"}}`
	req, err = http.NewRequestWithContext(t.Context(), http.MethodPatch, "/v1/web-research/providers/brave", strings.NewReader(badBrave))
	testutil.FailErr(t, "NewRequest brave", err)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("brave private endpoint status = %d body = %s", rec.Code, rec.Body.String())
	}
}
