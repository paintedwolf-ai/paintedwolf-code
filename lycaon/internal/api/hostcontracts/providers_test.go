package hostcontracts

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestPutProviderPartialUpdatePreservesHTTPRetry(t *testing.T) {
	discoveryServer := contractfixture.NewProvADiscoveryServer(t)
	shipYAML := contractfixture.FakeProvidersYAML(discoveryServer.URL)
	localYAML := strings.Replace(
		shipYAML,
		"max_retries: 1\n      max_wait_ms: 1000\n      backoff_ms: [1]",
		"max_retries: 2\n      max_wait_ms: 2000\n      backoff_ms: [10, 20]",
		1,
	)
	srv, base := contractfixture.NewProviderTestServerWithCatalogs(t, shipYAML, localYAML)

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodPatch, base+"/v1/providers/"+contractfixture.TestProviderID,
		strings.NewReader(`{"label":"Updated"}`),
	)
	testutil.FailErr(t, "new provider update", err)
	req.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(req)
	resp, err := http.DefaultClient.Do(req)
	testutil.FailErr(t, "update provider", err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.StatusCode, contractfixture.ReadBody(t, resp))
	}
	entry, ok := srv.Admin.Project.Verification.LLMService.Catalog.Get(contractfixture.TestProviderID)
	if !ok || entry.HTTPRetry.MaxRetries != 2 || len(entry.HTTPRetry.BackoffMs) != 2 {
		t.Fatalf("provider retry policy = %+v, found %v", entry.HTTPRetry, ok)
	}
}

func TestProviderEditsPreserveYAMLProtocolControls(t *testing.T) {
	discoveryServer := contractfixture.NewProvADiscoveryServer(t)
	shipYAML := contractfixture.FakeProvidersYAML(discoveryServer.URL)
	localYAML := strings.Replace(shipYAML, "    models:", `    reasoning_wire: reasoning_content
    rejection_reasons:
      - {status: 403, code_path: error.code, code: payment_required, reason: custom_payment_required}
    models:`, 1)
	localYAML = strings.Replace(localYAML, "      - id: model-x", "      - id: model-x\n        think_style: effort_levels\n        max_tokens: 8192\n        reasoning_effort_levels: {low: low, medium: high, high: max}", 1)
	srv, base := contractfixture.NewProviderTestServerWithCatalogs(t, shipYAML, localYAML)
	for _, body := range []string{`{"label":"Updated"}`, `{"models":[{"id":"model-x"}]}`} {
		contractfixture.PutProviderJSON(t, base, contractfixture.TestProviderID, body)
		entry, ok := srv.Admin.Project.Verification.LLMService.Catalog.Get(contractfixture.TestProviderID)
		if !ok || entry.LocalReasoningWire() != providerprofile.ReasoningWireContent || len(entry.LocalRejectionReasons()) != 1 || len(entry.Models) == 0 || entry.Models[0].ReasoningEffortLevels.Medium != "high" {
			t.Fatalf("provider edit lost YAML controls: %+v", entry)
		}
		if model := entry.Models[0]; model.ThinkStyle != "effort_levels" || model.MaxTokens != 8192 {
			t.Fatalf("provider edit lost YAML style or output cap: %+v", model)
		}
	}
}

// createProviderJSON creates a provider instance and returns its view.

func TestPutProviderSecretScreenTrustFollowsTheResolvedDestination(t *testing.T) {
	discoveryServer := contractfixture.NewProvADiscoveryServer(t)
	srv, base := contractfixture.NewProviderTestServerAt(t, discoveryServer.URL)
	stored := func() llm.CatalogEntry {
		t.Helper()
		entry, ok := srv.Admin.Project.Verification.LLMService.Catalog.Get(contractfixture.TestProviderID)
		if !ok {
			t.Fatal("provider missing from catalog")
		}
		return entry
	}

	if meta := contractfixture.PutProviderJSON(t, base, contractfixture.TestProviderID, `{"label":"Untouched"}`); meta.SecretScreenTrusted {
		t.Fatal("a fresh instance reported trust")
	}
	meta := contractfixture.PutProviderJSON(t, base, contractfixture.TestProviderID, `{"secret_screen_trusted":true}`)
	entry := stored()
	if !meta.SecretScreenTrusted || entry.SecretScreenTrust != entry.SecretDestinationID() {
		t.Fatalf("trusted: meta=%v stored=%q destination=%q", meta.SecretScreenTrusted, entry.SecretScreenTrust, entry.SecretDestinationID())
	}
	if meta := contractfixture.PutProviderJSON(t, base, contractfixture.TestProviderID, `{"label":"Renamed"}`); !meta.SecretScreenTrusted {
		t.Fatal("a rename withdrew trust; the label is presentation only")
	}

	meta = contractfixture.PutProviderJSON(t, base, contractfixture.TestProviderID, `{"base_url":"`+discoveryServer.URL+`/v2"}`)
	if meta.SecretScreenTrusted || stored().SecretScreenTrust != "" {
		t.Fatalf("repoint: meta=%v stored=%q, want trust withdrawn and cleared", meta.SecretScreenTrusted, stored().SecretScreenTrust)
	}
	if meta := contractfixture.PutProviderJSON(t, base, contractfixture.TestProviderID, `{"secret_screen_trusted":true}`); !meta.SecretScreenTrusted {
		t.Fatal("re-trusting the new destination failed")
	}
	if meta := contractfixture.PutProviderJSON(t, base, contractfixture.TestProviderID, `{"secret_screen_trusted":false}`); meta.SecretScreenTrusted || stored().SecretScreenTrust != "" {
		t.Fatal("withdrawing trust left it in place")
	}
}

func TestRefreshProviderModelsReportsProviderResponseFailure(t *testing.T) {
	var requests atomic.Int32
	discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(discoveryServer.Close)
	_, base := contractfixture.NewProviderTestServerAt(t, discoveryServer.URL)
	beforeRefresh := requests.Load()

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, base+"/v1/providers/"+contractfixture.TestProviderID+"/refresh", nil,
	)
	testutil.FailErr(t, "new refresh request", err)
	hostapi.WithTestAuth(req)
	resp, err := http.DefaultClient.Do(req)
	testutil.FailErr(t, "refresh request", err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	var apiErr wire.ErrorResponse
	testutil.FailErr(t, "decode error", json.NewDecoder(resp.Body).Decode(&apiErr))
	if apiErr.Code != "model_catalog_refresh_failed" {
		t.Fatalf("code = %q", apiErr.Code)
	}
	if got := requests.Load(); got != beforeRefresh+1 {
		t.Fatalf("model catalog requests = %d, want %d after one explicit refresh", got, beforeRefresh+1)
	}
}

func TestPutProviderRejectsKindChange(t *testing.T) {
	_, base := contractfixture.NewProviderTestServer(t)
	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodPatch, base+"/v1/providers/"+contractfixture.TestProviderID,
		strings.NewReader(`{"kind":"ollama"}`),
	)
	testutil.FailErr(t, "new provider request", err)
	req.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(req)
	resp, err := http.DefaultClient.Do(req)
	testutil.FailErr(t, "put provider", err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
}

func TestListProvidersCredentialNeverInJSON(t *testing.T) {
	_, base := contractfixture.NewProviderTestServer(t)
	secret := "sk-super-secret-never-expose"

	putReq, err := http.NewRequestWithContext(t.Context(), http.MethodPut, base+"/v1/providers/"+contractfixture.TestProviderID+"/credential",
		strings.NewReader(`{"api_key":`+contractfixture.JsonString(secret)+`}`))
	testutil.FailErr(t, "http.NewRequest failed", err)
	putReq.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(putReq)
	putResp, err := http.DefaultClient.Do(putReq)
	testutil.FailErr(t, "http.DefaultClient.Do failed", err)
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("put credential status = %d", putResp.StatusCode)
	}

	listResp, err := contractfixture.AuthedHTTPGet(base + "/v1/providers")
	testutil.FailErr(t, "authedHTTPGet failed", err)
	defer listResp.Body.Close()
	body, err := io.ReadAll(listResp.Body)
	testutil.FailErr(t, "io.ReadAll failed", err)
	if strings.Contains(string(body), secret) {
		t.Fatalf("GET /v1/providers leaked credential: %s", body)
	}
	if strings.Contains(string(body), `"api_key"`) {
		t.Fatalf("GET /v1/providers should not include api_key field: %s", body)
	}

	var res wire.ProviderListResponse
	if err := json.Unmarshal(body, &res); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	providers := res.Providers
	found := false
	for _, p := range providers {
		if p.ID == contractfixture.TestProviderID {
			found = true
			if !p.Configured {
				t.Fatalf("expected %s configured after credential put", contractfixture.TestProviderID)
			}
			if !p.CredentialPresent {
				t.Fatalf("expected %s credential_present after credential put", contractfixture.TestProviderID)
			}
			if p.BaseURL == "" {
				t.Fatal("expected base_url in provider meta")
			}
		}
	}
	if !found {
		t.Fatalf("%s not in provider list", contractfixture.TestProviderID)
	}
}

func TestPutProviderCredentialRejectsEmptyKey(t *testing.T) {
	_, base := contractfixture.NewProviderTestServer(t)

	contractfixture.CreateProviderJSON(t, base, `{"id":"key-required","base_url":"https://api.example.invalid/v1","requires_api_key":true}`)

	credentialReq, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPut,
		base+"/v1/providers/key-required/credential",
		strings.NewReader(`{"api_key":"   "}`),
	)
	testutil.FailErr(t, "credential request", err)
	credentialReq.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(credentialReq)
	credentialResp, err := http.DefaultClient.Do(credentialReq)
	testutil.FailErr(t, "put credential", err)
	defer credentialResp.Body.Close()
	if credentialResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("credential status = %d, want 400", credentialResp.StatusCode)
	}

	listResp, err := contractfixture.AuthedHTTPGet(base + "/v1/providers")
	testutil.FailErr(t, "list providers", err)
	defer listResp.Body.Close()
	var res wire.ProviderListResponse
	testutil.FailErr(t, "decode providers", json.NewDecoder(listResp.Body).Decode(&res))
	providers := res.Providers
	for _, provider := range providers {
		if provider.ID == "key-required" && provider.Configured {
			t.Fatal("empty credential marked key-required configured")
		}
	}
}

func TestUnknownProviderCommandsReturnNotFound(t *testing.T) {
	_, base := contractfixture.NewProviderTestServer(t)
	requests := []struct {
		method string
		path   string
	}{
		{method: http.MethodDelete, path: "/v1/providers/missing/credential"},
		{method: http.MethodPost, path: "/v1/providers/missing/test"},
	}
	for _, tc := range requests {
		req, err := http.NewRequestWithContext(t.Context(), tc.method, base+tc.path, strings.NewReader(`{}`))
		testutil.FailErr(t, "create unknown provider request", err)
		req.Header.Set("Content-Type", "application/json")
		hostapi.WithTestAuth(req)
		resp, err := http.DefaultClient.Do(req)
		testutil.FailErr(t, "run unknown provider request", err)
		if resp.StatusCode != http.StatusNotFound {
			body := contractfixture.ReadBody(t, resp)
			resp.Body.Close()
			t.Fatalf("%s %s status = %d body=%s", tc.method, tc.path, resp.StatusCode, body)
		}
		resp.Body.Close()
	}
}

func TestDeleteProviderRejectsAssignedInstanceAtomically(t *testing.T) {
	srv, base := contractfixture.NewProviderTestServer(t)
	credentialReq, err := http.NewRequestWithContext(
		t.Context(), http.MethodPut, base+"/v1/providers/"+contractfixture.TestProviderID+"/credential",
		strings.NewReader(`{"api_key":"keep-me"}`),
	)
	testutil.FailErr(t, "credential request", err)
	credentialReq.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(credentialReq)
	credentialResp, err := http.DefaultClient.Do(credentialReq)
	testutil.FailErr(t, "put credential", err)
	credentialResp.Body.Close()
	if credentialResp.StatusCode != http.StatusOK {
		t.Fatalf("credential status = %d", credentialResp.StatusCode)
	}

	deleteReq, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, base+"/v1/providers/"+contractfixture.TestProviderID, nil)
	testutil.FailErr(t, "delete request", err)
	hostapi.WithTestAuth(deleteReq)
	deleteResp, err := http.DefaultClient.Do(deleteReq)
	testutil.FailErr(t, "delete provider", err)
	defer deleteResp.Body.Close()
	if deleteResp.StatusCode != http.StatusConflict {
		t.Fatalf("delete status = %d body = %s", deleteResp.StatusCode, contractfixture.ReadBody(t, deleteResp))
	}
	if _, ok := srv.Admin.Project.Verification.LLMService.Catalog.Get(contractfixture.TestProviderID); !ok {
		t.Fatal("referenced provider was removed from catalog")
	}
	if credential, ok := srv.Admin.Project.Verification.LLMService.Credentials.Get(contractfixture.TestProviderID); !ok || credential != "keep-me" {
		t.Fatalf("credential after rejected delete = %q, %v", credential, ok)
	}
	if _, err := srv.Admin.Project.Verification.LLMService.Registry.Get(contractfixture.TestProviderID); err != nil {
		t.Fatalf("runtime provider after rejected delete: %v", err)
	}
}

// Partial provider updates preserve omitted stored models.

func TestPutProviderCreateWithoutModelsStaysEmpty(t *testing.T) {
	_, base := contractfixture.NewProviderTestServer(t)

	// Omitted models stay empty until discovery.
	meta := contractfixture.CreateProviderJSON(t, base, `{"id":"prov-a-2","base_url":"https://api.example.invalid/v1","kind":"prov-a","label":"Second"}`)
	if len(meta.Models) != 0 {
		t.Fatalf("create without models must not seed ship catalog: %+v", meta.Models)
	}
}

func TestPutProviderPartialUpdatePreservesModels(t *testing.T) {
	srv, base := contractfixture.NewProviderTestServer(t)

	// Seed full provider state.
	contractfixture.CreateProviderJSON(t, base, `{"id":"partial-test","base_url":"https://proxy.example/v1","models":[{"id":"keep-me","input_per_1k_nano_usd":1000000}]}`)

	// Update only base_url while preserving stored models.
	partial := `{"base_url":"https://proxy2.example/v1"}`
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPatch, base+"/v1/providers/partial-test", strings.NewReader(partial))
	req.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(req)
	resp, err := http.DefaultClient.Do(req)
	testutil.FailErr(t, "http.DefaultClient.Do failed", err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("partial put status = %d body = %s", resp.StatusCode, contractfixture.ReadBody(t, resp))
	}
	var meta wire.ProviderMeta
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		testutil.FailErr(t, "json.NewDecoder failed", err)
	}
	if meta.BaseURL != "https://proxy2.example/v1" {
		t.Fatalf("base_url not updated: %q", meta.BaseURL)
	}
	if len(meta.ConfiguredModels) != 1 || meta.ConfiguredModels[0].ID != "keep-me" {
		t.Fatalf("configured_models = %+v, want keep-me", meta.ConfiguredModels)
	}
	stored, ok := srv.Admin.Project.Verification.LLMService.Catalog.Get("partial-test")
	if !ok {
		t.Fatal("partial-test missing from catalog after partial update")
	}
	if len(stored.Models) != 1 || stored.Models[0].ID != "keep-me" {
		t.Fatalf("partial update wiped stored models: %+v", stored.Models)
	}

	// An explicit empty array clears stored models.
	clear := `{"base_url":"https://proxy2.example/v1","models":[]}`
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPatch, base+"/v1/providers/partial-test", strings.NewReader(clear))
	req.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(req)
	resp, err = http.DefaultClient.Do(req)
	testutil.FailErr(t, "http.DefaultClient.Do failed", err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear put status = %d body = %s", resp.StatusCode, contractfixture.ReadBody(t, resp))
	}
	stored, ok = srv.Admin.Project.Verification.LLMService.Catalog.Get("partial-test")
	if !ok {
		t.Fatal("partial-test missing from catalog after clear")
	}
	if len(stored.Models) != 0 {
		t.Fatalf("explicit empty array did not clear stored models: %+v", stored.Models)
	}
}

func TestPutProviderLabelOnlyUpdate(t *testing.T) {
	srv, base := contractfixture.NewProviderTestServer(t)

	contractfixture.CreateProviderJSON(t, base, `{"id":"label-test","base_url":"https://proxy.example/v1","label":"First","models":[{"id":"keep-me"}]}`)

	rename := `{"label":"Work"}`
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPatch, base+"/v1/providers/label-test", strings.NewReader(rename))
	req.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(req)
	resp, err := http.DefaultClient.Do(req)
	testutil.FailErr(t, "label put", err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("label put status = %d body = %s", resp.StatusCode, contractfixture.ReadBody(t, resp))
	}
	var meta wire.ProviderMeta
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		testutil.FailErr(t, "decode meta", err)
	}
	if meta.Label != "Work" {
		t.Fatalf("label = %q, want Work", meta.Label)
	}
	if meta.BaseURL != "https://proxy.example/v1" {
		t.Fatalf("base_url changed on label-only put: %q", meta.BaseURL)
	}
	stored, ok := srv.Admin.Project.Verification.LLMService.Catalog.Get("label-test")
	if !ok {
		t.Fatal("label-test missing after rename")
	}
	if stored.Label != "Work" || len(stored.Models) != 1 {
		t.Fatalf("catalog after rename: %+v", stored)
	}

	clear := `{"label":""}`
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPatch, base+"/v1/providers/label-test", strings.NewReader(clear))
	req.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(req)
	resp, err = http.DefaultClient.Do(req)
	testutil.FailErr(t, "clear label put", err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("clear label status = %d, want 400", resp.StatusCode)
	}
	stored, _ = srv.Admin.Project.Verification.LLMService.Catalog.Get("label-test")
	if stored.Label != "Work" {
		t.Fatalf("empty label cleared previous: %q", stored.Label)
	}
}

func TestPutProviderAndModelPolicy(t *testing.T) {
	_, base := contractfixture.NewProviderTestServer(t)

	contractfixture.CreateProviderJSON(t, base, `{"id":"my-proxy","base_url":"https://proxy.example/v1","models":[{"id":"custom-model","input_per_1k_nano_usd":1000000,"output_per_1k_nano_usd":2000000}]}`)

	// Model policy uses only fixture ids.
	policyBody := `{
        "coordinator": {"provider_id":"` + contractfixture.TestProviderID + `","model":"model-x"},
        "lite":        {"provider_id":"` + contractfixture.TestProviderID + `","model":"model-y"},
        "agent_pool":  {"selection":"first","models":[{"provider_id":"` + contractfixture.TestProviderID + `","model":"model-y"}]}
    }`
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPatch, base+"/v1/settings/model-policy", strings.NewReader(policyBody))
	req.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(req)
	polResp, err := http.DefaultClient.Do(req)
	testutil.FailErr(t, "http.DefaultClient.Do failed", err)
	defer polResp.Body.Close()
	if polResp.StatusCode != http.StatusOK {
		t.Fatalf("put model policy status = %d body = %s", polResp.StatusCode, contractfixture.ReadBody(t, polResp))
	}

	getResp, err := contractfixture.AuthedHTTPGet(base + "/v1/settings/model-policy")
	testutil.FailErr(t, "authedHTTPGet failed", err)
	defer getResp.Body.Close()
	var policy wire.ModelPolicy
	if err := json.NewDecoder(getResp.Body).Decode(&policy); err != nil {
		testutil.FailErr(t, "json.NewDecoder failed", err)
	}
	if policy.AgentPool.Selection != "first" {
		t.Fatalf("selection = %q", policy.AgentPool.Selection)
	}
}

func TestPutModelPolicyCoordinatorPatchLeavesLite(t *testing.T) {
	_, base := contractfixture.NewProviderTestServer(t)
	seed := `{
        "coordinator": {"provider_id":"` + contractfixture.TestProviderID + `","model":"model-x"},
        "lite":        {"provider_id":"` + contractfixture.TestProviderID + `","model":"model-y"},
        "agent_pool":  {"selection":"first","models":[{"provider_id":"` + contractfixture.TestProviderID + `","model":"model-x"}]}
    }`
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPatch, base+"/v1/settings/model-policy", strings.NewReader(seed))
	testutil.FailErr(t, "seed request", err)
	req.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(req)
	resp, err := http.DefaultClient.Do(req)
	testutil.FailErr(t, "seed put", err)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("seed status = %d", resp.StatusCode)
	}

	patch := `{
        "coordinator": {"provider_id":"` + contractfixture.TestProviderID + `","model":"model-y"},
        "agent_pool":  {"selection":"first","models":[{"provider_id":"` + contractfixture.TestProviderID + `","model":"model-y"}]}
    }`
	req, err = http.NewRequestWithContext(t.Context(), http.MethodPatch, base+"/v1/settings/model-policy", strings.NewReader(patch))
	testutil.FailErr(t, "patch request", err)
	req.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(req)
	resp, err = http.DefaultClient.Do(req)
	testutil.FailErr(t, "patch put", err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch status = %d body = %s", resp.StatusCode, contractfixture.ReadBody(t, resp))
	}
	var policy wire.ModelPolicy
	if err := json.NewDecoder(resp.Body).Decode(&policy); err != nil {
		testutil.FailErr(t, "decode patch response", err)
	}
	if policy.Coordinator.Model != "model-y" {
		t.Fatalf("coordinator = %+v", policy.Coordinator)
	}
	if policy.Lite.Model != "model-y" || policy.Lite.ProviderID != contractfixture.TestProviderID {
		t.Fatalf("lite = %+v, coordinator patch must leave lite stored", policy.Lite)
	}
}

func TestModelPolicyProjectOverlayInheritsAndTracksHTTP(t *testing.T) {
	srv, base := contractfixture.NewProviderTestServer(t)
	projectDir := t.TempDir()
	proj := contractfixture.CreateProjectForTest(t, srv, projectDir)

	decodePolicy := func(t *testing.T, resp *http.Response) wire.ModelPolicy {
		t.Helper()
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d body = %s", resp.StatusCode, contractfixture.ReadBody(t, resp))
		}
		var p wire.ModelPolicy
		if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
			testutil.FailErr(t, "decode model policy", err)
		}
		return p
	}
	putPolicy := func(t *testing.T, url, body string) wire.ModelPolicy {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPatch, url, strings.NewReader(body))
		testutil.FailErr(t, "NewRequest", err)
		req.Header.Set("Content-Type", "application/json")
		hostapi.WithTestAuth(req)
		resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // decodePolicy closes the body
		testutil.FailErr(t, "Do", err)
		return decodePolicy(t, resp)
	}
	refJSON := func(provider, model string) string {
		return `{"provider_id":` + contractfixture.JsonString(provider) + `,"model":` + contractfixture.JsonString(model) + `}`
	}
	// A null slot clears the overlay's assignment.
	emptyRef := `null`
	model := func(ref *wire.ModelRefDTO) string {
		if ref == nil {
			return ""
		}
		return ref.Model
	}
	fullPolicy := func(coord, lite, worker string) string {
		return `{
			"coordinator": ` + refJSON(contractfixture.TestProviderID, coord) + `,
			"lite": ` + refJSON(contractfixture.TestProviderID, lite) + `,
			"agent_pool": {"selection":"first","models":[` + refJSON(contractfixture.TestProviderID, worker) + `]}
		}`
	}

	projectURL := base + "/v1/settings/model-policy?project_id=" + proj.ID
	globalURL := base + "/v1/settings/model-policy"

	// New project overlays start empty.
	getResp, err := contractfixture.AuthedHTTPGet(projectURL) //nolint:bodyclose // decodePolicy closes the body
	testutil.FailErr(t, "GET project overlay", err)
	overlay := decodePolicy(t, getResp)
	if overlay.Coordinator != nil {
		t.Fatalf("new project overlay coordinator = %+v want unassigned", overlay.Coordinator)
	}
	if overlay.Lite != nil || len(overlay.AgentPool.Models) != 0 {
		t.Fatalf("new project overlay must be empty ---: %+v", overlay)
	}

	putPolicy(t, globalURL, fullPolicy("model-x", "model-y", "model-x"))

	// Empty overlays inherit effective settings.
	effective, err := srv.Admin.Project.Verification.LLMService.Policy.Get(llm.SettingsScopeProject, projectDir)
	testutil.FailErr(t, "get effective project policy", err)
	if effective.Coordinator.Model != "model-x" {
		t.Fatalf("effective coordinator = %+v want model-x from global", effective.Coordinator)
	}
	getResp, err = contractfixture.AuthedHTTPGet(projectURL) //nolint:bodyclose // decodePolicy closes the body
	testutil.FailErr(t, "GET project overlay after global", err)
	overlay = decodePolicy(t, getResp)
	if overlay.Coordinator != nil {
		t.Fatalf("HTTP project GET must return the unassigned overlay, got %+v", overlay.Coordinator)
	}

	// effective=true returns the merged projection.
	getResp, err = contractfixture.AuthedHTTPGet(projectURL + "&effective=true") //nolint:bodyclose // decodePolicy closes the body
	testutil.FailErr(t, "GET project effective policy", err)
	effectiveDTO := decodePolicy(t, getResp)
	if model(effectiveDTO.Coordinator) != "model-x" {
		t.Fatalf("effective=true coordinator = %+v want model-x from global", effectiveDTO.Coordinator)
	}
	getResp, err = contractfixture.AuthedHTTPGet(globalURL + "?effective=true") //nolint:bodyclose // decodePolicy closes the body
	testutil.FailErr(t, "GET global effective policy", err)
	effectiveDTO = decodePolicy(t, getResp)
	if model(effectiveDTO.Coordinator) != "model-x" {
		t.Fatalf("global effective=true coordinator = %+v want model-x", effectiveDTO.Coordinator)
	}

	putPolicy(t, globalURL, fullPolicy("model-y", "model-y", "model-y"))
	effective, err = srv.Admin.Project.Verification.LLMService.Policy.Get(llm.SettingsScopeProject, projectDir)
	testutil.FailErr(t, "get updated project policy", err)
	if effective.Coordinator.Model != "model-y" {
		t.Fatalf("empty project must track global change: %+v", effective.Coordinator)
	}

	// Unset project slots continue to inherit.
	overrideBody := `{
		"coordinator": ` + refJSON(contractfixture.TestProviderID, "model-x") + `,
		"lite": ` + emptyRef + `,
		"agent_pool": {"selection":"first","models":[]}
	}`
	saved := putPolicy(t, projectURL, overrideBody)
	if model(saved.Coordinator) != "model-x" {
		t.Fatalf("PUT project overlay coordinator = %+v", saved.Coordinator)
	}
	if saved.Lite != nil {
		t.Fatalf("unset slots must stay unassigned in response: %+v", saved)
	}

	putPolicy(t, globalURL, fullPolicy("model-y", "model-y", "model-y"))
	effective, err = srv.Admin.Project.Verification.LLMService.Policy.Get(llm.SettingsScopeProject, projectDir)
	testutil.FailErr(t, "get overridden project policy", err)
	if effective.Coordinator.Model != "model-x" {
		t.Fatalf("explicit override must not track: %+v", effective.Coordinator)
	}
	if effective.Lite.Model != "model-y" {
		t.Fatalf("--- lite slot must track global: %+v", effective.Lite)
	}

	// Clearing every slot removes the overlay.
	cleared := putPolicy(t, projectURL, `{
		"coordinator": `+emptyRef+`,
		"lite": `+emptyRef+`,
		"agent_pool": {"selection":"first","models":[]}
	}`)
	if cleared.Coordinator != nil {
		t.Fatalf("cleared overlay = %+v", cleared)
	}
	if _, err := os.Stat(filepath.Join(projectDir, settingsoverlay.DirName(), "model-policy.yaml")); !os.IsNotExist(err) {
		t.Fatalf("empty project overlay must delete file: err=%v", err)
	}
	effective, err = srv.Admin.Project.Verification.LLMService.Policy.Get(llm.SettingsScopeProject, projectDir)
	testutil.FailErr(t, "get cleared project policy", err)
	if effective.Coordinator.Model != "model-y" {
		t.Fatalf("after clear, effective must track global again: %+v", effective.Coordinator)
	}
}

func TestPostProviderTestWithMockBackend(t *testing.T) {
	srv, base := contractfixture.NewProviderTestServer(t)

	mockBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && (r.URL.Path == "/models" || strings.HasSuffix(r.URL.Path, "/models")) {
			_, _ = w.Write([]byte(`{"data":[{"id":"test-model","object":"model"}]}`))
			return
		}
		t.Errorf("connection test must only list models, got %s %s", r.Method, r.URL.Path)
		http.Error(w, "inference is not allowed during connection tests", http.StatusMethodNotAllowed)
	}))
	t.Cleanup(mockBackend.Close)

	contractfixture.CreateProviderJSON(t, base, `{"id":"test-openai","base_url":`+contractfixture.JsonString(mockBackend.URL)+`,"models":[{"id":"test-model"}]}`)

	credReq, _ := http.NewRequestWithContext(t.Context(), http.MethodPut, base+"/v1/providers/test-openai/credential",
		strings.NewReader(`{"api_key":"test-key"}`))
	credReq.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(credReq)
	credResp, err := http.DefaultClient.Do(credReq)
	testutil.FailErr(t, "http.DefaultClient.Do failed", err)
	credResp.Body.Close()

	testResp, err := contractfixture.AuthedHTTPPost(base+"/v1/providers/test-openai/test", "application/json", `{}`)
	testutil.FailErr(t, "authedHTTPPost failed", err)
	defer testResp.Body.Close()
	raw, err := io.ReadAll(testResp.Body)
	testutil.FailErr(t, "io.ReadAll failed", err)
	if strings.Contains(string(raw), "test-key") {
		t.Fatalf("provider test response leaked credential: %s", raw)
	}
	var result wire.ProviderProbeResult
	if err := json.Unmarshal(raw, &result); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if !result.OK {
		t.Fatalf("test failed: %+v", result)
	}
	if result.LatencyMs < 0 {
		t.Fatalf("latency = %d", result.LatencyMs)
	}
	_ = srv
}
