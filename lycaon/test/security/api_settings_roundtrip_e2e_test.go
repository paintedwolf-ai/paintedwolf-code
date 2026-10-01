package security

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestSettingsRoundTripE2E(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	h := wiring.BuildForTest(t)
	httpSrv := httptest.NewServer(h.Server)
	t.Cleanup(httpSrv.Close)
	base := httpSrv.URL

	initial := openAPIGetJSON[wire.ProviderListResponse](t, base, "/v1/providers", nil, http.StatusOK).Providers
	if len(initial) != 0 {
		t.Fatalf("fresh config dir must expose no provider instances, got %+v", initial)
	}
	kinds := settingsGetJSON[wire.ProviderKindListResponse](t, base, "/v1/provider-kinds").Kinds
	if len(kinds) == 0 {
		t.Fatal("expected ship provider kinds for the Add AI provider picker")
	}
	foundOllama := false
	for _, k := range kinds {
		if k.Kind == "ollama" {
			foundOllama = true
		}
	}
	if !foundOllama {
		t.Fatalf("ollama not in ship kind catalog: %+v", kinds)
	}

	full := `{"id":"test-merge","base_url":"https://x.example/v1","models":[{"id":"keep-me","input_per_1k_nano_usd":1000000}]}`
	created := openAPIPostJSON[wire.ProviderMeta](t, base, "/v1/providers", nil, full, http.StatusCreated)
	if created.ID != "test-merge" {
		t.Fatalf("created id = %q, want test-merge", created.ID)
	}
	if len(created.Models) != 0 {
		t.Fatalf("effective models must stay empty without live discovery: %+v", created.Models)
	}

	partial := `{"base_url":"https://x2.example/v1"}`
	patched := openAPIPatchJSON[wire.ProviderMeta](t, base, "/v1/providers/{provider_id}",
		map[string]string{"provider_id": "test-merge"}, partial, http.StatusOK)
	if patched.BaseURL != "https://x2.example/v1" {
		t.Fatalf("partial PATCH base_url = %q", patched.BaseURL)
	}

	afterPartial := settingsGetJSON[wire.ProviderListResponse](t, base, "/v1/providers").Providers
	merged := findProvider(afterPartial, "test-merge")
	if merged == nil {
		t.Fatalf("GET after partial PATCH lost the instance: %+v", afterPartial)
	}
	if merged.BaseURL != "https://x2.example/v1" {
		t.Fatalf("GET after partial PATCH base_url = %q", merged.BaseURL)
	}

	credResp := openAPIPutJSON[wire.ProviderMeta](t, base, "/v1/providers/{provider_id}/credential",
		map[string]string{"provider_id": "test-merge"}, `{"api_key":"sk-test-1234"}`, http.StatusOK)
	if !credResp.CredentialPresent {
		t.Fatalf("PUT credential should report credential_present: true, got %+v", credResp)
	}

	listAfterCred := settingsGetRaw(t, base, "/v1/providers")
	if strings.Contains(listAfterCred, "sk-test-1234") {
		t.Fatalf("provider list leaked credential value: %s", listAfterCred)
	}
	if strings.Contains(listAfterCred, "\"api_key\"") {
		t.Fatalf("provider list exposed api_key field: %s", listAfterCred)
	}

	openAPIDo(t, base, http.MethodDelete, "/v1/providers/{provider_id}/credential",
		map[string]string{"provider_id": "test-merge"}, "", http.StatusNoContent)
	afterDelete := findProvider(settingsGetJSON[wire.ProviderListResponse](t, base, "/v1/providers").Providers, "test-merge")
	if afterDelete == nil || afterDelete.CredentialPresent {
		t.Fatalf("provider credential should be removed after credential DELETE: %+v", afterDelete)
	}

	testSettingsModelPolicy(t, base)
	testSettingsApprovals(t, base)
	testSettingsLimits(t, base)
}

func testSettingsModelPolicy(t *testing.T, base string) {
	t.Helper()
	_ = openAPIGetJSON[wire.ModelPolicy](t, base, "/v1/settings/model-policy", nil, http.StatusOK)

	providerID, _ := seedTestProvider(t, base, false)
	newPolicy := wire.ModelPolicy{
		Coordinator: &wire.ModelRefDTO{ProviderID: providerID, Model: testProviderModel},
		Lite:        &wire.ModelRefDTO{ProviderID: providerID, Model: testProviderModel},
		AgentPool: wire.AgentPoolDTO{
			Selection: "round_robin",
			Models: []wire.ModelRefDTO{
				{ProviderID: providerID, Model: testProviderModel},
			},
		},
	}
	body, err := json.Marshal(newPolicy)
	testutil.FailErr(t, "json.Marshal failed", err)
	putPolicy := settingsPatchJSON[wire.ModelPolicy](t, base, "/v1/settings/model-policy", string(body), http.StatusOK)
	if putPolicy.Coordinator.Model != testProviderModel {
		t.Fatalf("PATCH model-policy did not apply: %+v", putPolicy)
	}
	getAfter := settingsGetJSON[wire.ModelPolicy](t, base, "/v1/settings/model-policy")
	if getAfter.Coordinator.Model != testProviderModel {
		t.Fatalf("GET after PATCH model-policy lost change: %+v", getAfter)
	}

	coordOnly, err := json.Marshal(map[string]any{
		"coordinator": wire.ModelRefDTO{ProviderID: providerID, Model: testProviderModel},
		"agent_pool": wire.AgentPoolDTO{
			Selection: "round_robin",
			Models:    []wire.ModelRefDTO{{ProviderID: providerID, Model: testProviderModel}},
		},
	})
	testutil.FailErr(t, "marshal coordinator patch", err)
	patched := settingsPatchJSON[wire.ModelPolicy](t, base, "/v1/settings/model-policy", string(coordOnly), http.StatusOK)
	if patched.Lite.Model != testProviderModel {
		t.Fatalf("coordinator patch cleared lite: %+v", patched)
	}

	status, errBody := settingsPatch(t, base, "/v1/settings/model-policy", `{}`)
	if status != http.StatusBadRequest {
		t.Fatalf("empty PATCH model-policy status = %d, want 400; body=%s", status, errBody)
	}
	var errEnv wire.ErrorResponse
	if err := json.Unmarshal(errBody, &errEnv); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if errEnv.Code != "invalid_request" {
		t.Fatalf("empty model-policy error code = %q, want invalid_request", errEnv.Code)
	}
}

func testSettingsApprovals(t *testing.T, base string) {
	t.Helper()
	applied := settingsPatchJSON[wire.ApprovalConfigResponse](t, base,
		"/v1/settings/approvals",
		`{"rules":[],"approval_posture":"strict"}`, http.StatusOK)
	if applied.ApprovalPosture != "strict" {
		t.Fatalf("patch posture = %q, want strict", applied.ApprovalPosture)
	}
	got := settingsGetJSON[wire.ApprovalConfigResponse](t, base, "/v1/settings/approvals")
	if got.ApprovalPosture != "strict" {
		t.Fatalf("get posture = %q, want strict", got.ApprovalPosture)
	}

	badStatus, badBody := settingsPatch(t, base, "/v1/settings/approvals",
		`{"rules":[],"approval_posture":"careful"}`)
	if badStatus != http.StatusBadRequest {
		t.Fatalf("invalid posture PATCH status = %d, want 400; body=%s", badStatus, badBody)
	}
}

func testSettingsLimits(t *testing.T, base string) {
	t.Helper()
	limits := settingsGetJSON[wire.SettingsLimitsResponse](t, base, "/v1/settings/limits")
	newLimit := wire.SettingsLimitsPatch{MaxIterations: new(limits.MaxIterations + 5)}
	bodyL, err := json.Marshal(newLimit)
	testutil.FailErr(t, "marshal limits", err)
	putLimits := settingsPatchJSON[wire.SettingsLimitsResponse](t, base,
		"/v1/settings/limits", string(bodyL), http.StatusOK)
	if putLimits.MaxIterations != *newLimit.MaxIterations {
		t.Fatalf("PATCH limits did not apply: %+v", putLimits)
	}
	getLimits := settingsGetJSON[wire.SettingsLimitsResponse](t, base, "/v1/settings/limits")
	if getLimits.MaxIterations != *newLimit.MaxIterations {
		t.Fatalf("GET after PATCH limits lost change: %+v", getLimits)
	}
	if getLimits.WorkerToolBudgetDefault != limits.WorkerToolBudgetDefault || getLimits.WorkerToolBudgetMin != limits.WorkerToolBudgetMin || getLimits.WorkerToolBudgetMax != limits.WorkerToolBudgetMax {
		t.Fatalf("partial patch changed omitted worker budgets: before=%+v after=%+v", limits, getLimits)
	}
	empty := settingsPatchJSON[wire.SettingsLimitsResponse](t, base, "/v1/settings/limits", `{}`, http.StatusOK)
	if empty.MaxIterations != getLimits.MaxIterations {
		t.Fatalf("empty patch changed limit: %+v", empty)
	}
	zero := settingsPatchJSON[wire.SettingsLimitsResponse](t, base, "/v1/settings/limits", `{"max_coordinator_loop_cycles":0}`, http.StatusOK)
	if zero.MaxCoordinatorLoopCycles != 0 || zero.MaxIterations != getLimits.MaxIterations {
		t.Fatalf("explicit zero did not preserve unrelated limit: %+v", zero)
	}
	reset := settingsPatchJSON[wire.SettingsLimitsResponse](t, base, "/v1/settings/limits", `{"max_iterations":null}`, http.StatusOK)
	if reset.MaxIterations != limits.MaxIterations || reset.MaxCoordinatorLoopCycles != 0 {
		t.Fatalf("null reset did not restore default and preserve explicit zero: %+v", reset)
	}

}

func findProvider(list []wire.ProviderMeta, id string) *wire.ProviderMeta {
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

func settingsGetRaw(t *testing.T, base, path string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+path, nil)
	testutil.FailErr(t, "http.NewRequest failed", err)
	req.Header.Set("Authorization", api.TestAuthHeader())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d body = %s", path, resp.StatusCode, body)
	}
	return string(body)
}

func settingsGetJSON[T any](t *testing.T, base, path string) T {
	t.Helper()
	body := settingsGetRaw(t, base, path)
	var v T
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode GET %s: %v body=%s", path, err, body)
	}
	return v
}
