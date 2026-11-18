package security

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
	"slices"
)

func TestWebResearchCredentialRoundtrip(t *testing.T) {
	h := wiring.BuildForTest(t)
	configDir, err := configdir.UserConfigDir()
	testutil.FailErr(t, "UserConfigDir", err)
	credPath := filepath.Join(configDir, "credential-vault.age")
	httpSrv := httptest.NewServer(h.Server)
	t.Cleanup(httpSrv.Close)
	base := httpSrv.URL

	getStatus := func() wire.WebResearchProvidersResponse {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/v1/web-research/providers", nil)
		testutil.FailErr(t, "NewRequest", err)
		req.Header.Set("Authorization", "Bearer "+h.APIToken)
		resp, err := http.DefaultClient.Do(req)
		testutil.FailErr(t, "Do", err)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET status = %d", resp.StatusCode)
		}
		var body wire.WebResearchProvidersResponse
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			testutil.FailErr(t, "Decode", err)
		}
		return body
	}

	credentialStored := func(status wire.WebResearchProvidersResponse, slot string) bool {
		for _, p := range status.Providers {
			if p.CredentialSlot == slot {
				return p.CredentialPresent
			}
		}
		return false
	}

	st := getStatus()
	if credentialStored(st, "brave-search") || credentialStored(st, "marginalia") {
		t.Fatalf("initial status = %+v want both false", st.Providers)
	}

	put := func(id, key string) {
		t.Helper()
		payload, mErr := json.Marshal(map[string]string{"api_key": key})
		testutil.FailErr(t, "marshal credentials", mErr)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, base+"/v1/web-research/providers/"+id+"/credential", bytes.NewReader(payload))
		testutil.FailErr(t, "NewRequest", err)
		req.Header.Set("Authorization", "Bearer "+h.APIToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		testutil.FailErr(t, "Do", err)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT %s status = %d", id, resp.StatusCode)
		}
	}

	// Credentials are addressed by provider; brave reads the brave-search slot.
	put("brave", "brave-test-key")
	if !credentialStored(getStatus(), "brave-search") {
		t.Fatal("expected brave configured")
	}

	data, err := os.ReadFile(credPath)
	testutil.FailErr(t, "ReadFile", err)
	if bytes.Contains(data, []byte("brave-test-key")) {
		t.Fatal("credential vault must encrypt the API key at rest")
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/v1/web-research/providers", nil)
	testutil.FailErr(t, "NewRequest", err)
	req.Header.Set("Authorization", "Bearer "+h.APIToken)
	resp, err := http.DefaultClient.Do(req)
	testutil.FailErr(t, "Do", err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	testutil.FailErr(t, "ReadAll", err)
	if bytes.Contains(body, []byte("brave-test-key")) {
		t.Fatalf("GET must not return raw api key: %s", string(body))
	}

	del := func(id string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, base+"/v1/web-research/providers/"+id+"/credential", nil)
		testutil.FailErr(t, "NewRequest", err)
		req.Header.Set("Authorization", "Bearer "+h.APIToken)
		resp, err := http.DefaultClient.Do(req)
		testutil.FailErr(t, "Do", err)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("DELETE %s status = %d", id, resp.StatusCode)
		}
	}

	del("brave")
	if credentialStored(getStatus(), "brave-search") {
		t.Fatal("expected brave cleared")
	}
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
