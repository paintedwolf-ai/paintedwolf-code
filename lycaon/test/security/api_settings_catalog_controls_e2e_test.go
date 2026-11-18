package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func settingsControlsBase(t *testing.T) string {
	t.Helper()
	h := wiring.BuildForTest(t)
	httpSrv := httptest.NewServer(h.Server)
	t.Cleanup(httpSrv.Close)
	return httpSrv.URL
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	testutil.FailErr(t, "encode request body", err)
	return string(raw)
}

func TestWebResearchProviderMembershipFollowsCatalogE2E(t *testing.T) {
	base := settingsControlsBase(t)
	const providersPath = "/v1/web-research/providers"
	const settingsPath = "/v1/settings/web-research"
	readProviders := func() wire.WebResearchProvidersResponse {
		t.Helper()
		return openAPIGetJSON[wire.WebResearchProvidersResponse](t, base, providersPath, nil, http.StatusOK)
	}
	readEnabled := func() []string {
		t.Helper()
		return openAPIGetJSON[wire.WebResearchSettings](t, base, settingsPath, nil, http.StatusOK).EnabledProviders
	}
	setEnabled := func(enabled []string) {
		t.Helper()
		openAPIPatchJSON[wire.WebResearchSettings](t, base, settingsPath, nil,
			mustJSON(t, map[string][]string{"enabled_providers": enabled}), http.StatusOK)
	}

	original := readProviders()
	originalEnabled := readEnabled()
	ids := []string{"direct"}
	bundled := map[string]bool{}
	for _, p := range original.Providers {
		ids = append(ids, string(p.ID))
		if p.Kind == wire.WebResearchProviderKindKeyless && p.DefaultEnabled {
			bundled[string(p.ID)] = true
		}
	}
	if len(ids) <= 1 {
		t.Fatalf("catalog lists no web research providers: %v", ids)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("provider id %q listed twice: %v", id, ids)
		}
		seen[id] = true
	}

	for _, id := range ids {
		for _, enabled := range [][]string{{id}, {}} {
			setEnabled(enabled)
			// Bundled keyless providers are always on, so they never appear in the set.
			want := enabled
			if bundled[id] {
				want = []string{}
			}
			// A nil slice means the host answered null, not an empty set.
			if got := readEnabled(); got == nil || !slices.Equal(got, want) {
				t.Fatalf("%s: enabled_providers = %#v after PATCH %#v, want %#v", id, got, enabled, want)
			}
		}
	}

	for _, tc := range []struct {
		id     string
		config map[string]string
	}{
		{"google_cse", map[string]string{"search_engine_id": "disposable-search-engine-id"}},
		{"searxng", map[string]string{"endpoint": "http://192.168.240.2:18080"}},
	} {
		baseline := map[string]string{}
		for _, p := range original.Providers {
			if string(p.ID) == tc.id && p.Config != nil {
				baseline = p.Config
			}
		}
		path := map[string]string{"provider_id": tc.id}
		openAPIPatchJSON[wire.WebResearchProviderMeta](t, base, "/v1/web-research/providers/{provider_id}", path,
			mustJSON(t, map[string]any{"config": tc.config}), http.StatusOK)
		var stored map[string]string
		for _, p := range readProviders().Providers {
			if string(p.ID) == tc.id {
				stored = p.Config
			}
		}
		for k, v := range tc.config {
			if stored[k] != v {
				t.Fatalf("%s: config[%s] = %q, want %q (config %v)", tc.id, k, stored[k], v, stored)
			}
		}
		openAPIPatchJSON[wire.WebResearchProviderMeta](t, base, "/v1/web-research/providers/{provider_id}", path,
			mustJSON(t, map[string]any{"config": baseline}), http.StatusOK)
	}
	setEnabled(originalEnabled)
}

func TestProviderSettingsSurvivePartialUpdatesE2E(t *testing.T) {
	base := settingsControlsBase(t)
	const providerPath = "/v1/providers/{provider_id}"
	update := func(id string, req wire.UpdateProviderRequest) wire.ProviderMeta {
		t.Helper()
		return openAPIPatchJSON[wire.ProviderMeta](t, base, providerPath, map[string]string{"provider_id": id},
			mustJSON(t, req), http.StatusOK)
	}

	const azureID, bedrockID = "settings-azure-fixture", "settings-bedrock-fixture"
	models := []wire.ProviderModelConfig{{ID: "fixture-deployment", PricedAs: "fixture-model"}}
	openAPIPostJSON[wire.ProviderMeta](t, base, "/v1/providers", nil, mustJSON(t, wire.CreateProviderRequest{
		ID: azureID, Kind: "azure", Label: "Deployment fixture", BaseURL: "https://fixture.openai.azure.com", Models: models,
	}), http.StatusCreated)
	azure := update(azureID, wire.UpdateProviderRequest{Label: new("Renamed deployment fixture")})
	if len(azure.ConfiguredModels) != len(models) ||
		azure.ConfiguredModels[0].ID != models[0].ID || azure.ConfiguredModels[0].PricedAs != models[0].PricedAs {
		t.Fatalf("label update lost deployment rows: configured_models = %+v, want %+v", azure.ConfiguredModels, models)
	}

	openAPIPostJSON[wire.ProviderMeta](t, base, "/v1/providers", nil, mustJSON(t, wire.CreateProviderRequest{
		ID: bedrockID, Kind: "bedrock", Label: "Credential mode fixture", BaseURL: "us-east-1", RequiresAPIKey: new(true),
	}), http.StatusCreated)
	for _, required := range []bool{false, true} {
		update(bedrockID, wire.UpdateProviderRequest{RequiresAPIKey: new(required)})
		provider := update(bedrockID, wire.UpdateProviderRequest{Label: new("Retained credential mode")})
		if provider.RequiresAPIKey != required {
			t.Fatalf("label update changed requires_api_key: got %t, want %t", provider.RequiresAPIKey, required)
		}
		if provider.AmbientAuth != "aws-sdk-chain" {
			t.Fatalf("ambient_auth = %q, want aws-sdk-chain", provider.AmbientAuth)
		}
	}

	for _, id := range []string{azureID, bedrockID} {
		openAPIDo(t, base, http.MethodDelete, providerPath, map[string]string{"provider_id": id}, "", http.StatusNoContent)
	}
}

func TestDetectionPackEnableSettingRetainedE2E(t *testing.T) {
	base := settingsControlsBase(t)
	read := func() wire.DetectionPackList {
		t.Helper()
		return openAPIGetJSON[wire.DetectionPackList](t, base, "/v1/detection-packs", nil, http.StatusOK)
	}
	original := read()
	if len(original.Packs) == 0 {
		t.Fatal("detection catalog lists no packs")
	}
	for _, pack := range original.Packs {
		path := map[string]string{"pack_id": pack.ID}
		for _, enabled := range []bool{false, true, pack.Enabled} {
			openAPIDo(t, base, http.MethodPatch, "/v1/detection-packs/{pack_id}", path,
				mustJSON(t, wire.UpdateDetectionPackRequest{Enabled: new(enabled)}), http.StatusOK)
			packs := read().Packs
			i := slices.IndexFunc(packs, func(p wire.DetectionPack) bool { return p.ID == pack.ID })
			if i < 0 {
				t.Fatalf("%s: pack missing after PATCH enabled=%t", pack.ID, enabled)
			}
			if got := packs[i].Enabled; got != enabled {
				t.Fatalf("%s: enabled = %t after PATCH enabled=%t", pack.ID, got, enabled)
			}
		}
	}
}
