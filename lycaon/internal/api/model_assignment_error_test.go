package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// withTestUserNotices renders coded errors through the bundled user notices.
func withTestUserNotices(t *testing.T) testDeps {
	t.Helper()
	notices := testUserNotices(t)
	return func(d *Dependencies) { d.Core.UserNotices = notices }
}

func TestModelPolicyCatalogFailureAndRecovery(t *testing.T) {
	var ready atomic.Bool
	var completions atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			completions.Add(1)
			http.NotFound(w, r)
			return
		}
		if !ready.Load() {
			http.Error(w, "private provider diagnostic", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"model-x"},{"id":"model-y"}]}`))
	}))
	t.Cleanup(upstream.Close)
	server, base := newProviderTestServerAt(t, upstream.URL, withTestUserNotices(t))
	before, err := server.Admin.Project.Verification.LLMService.Policy.Overlay(llm.SettingsScopeGlobal, "")
	testutil.FailErr(t, "read initial policy", err)
	apply := func(model string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPatch, base+"/v1/settings/model-policy", strings.NewReader(`{"coordinator":{"provider_id":"prov-a","model":"`+model+`"}}`))
		request.Header.Set("Authorization", "Bearer "+TestAPIToken)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		return response
	}
	response := apply("model-x")
	var body wire.ErrorResponse
	testutil.FailErr(t, "decode unavailable response", json.Unmarshal(response.Body.Bytes(), &body))
	if response.Code != http.StatusServiceUnavailable || body.Code != wire.ApiErrorCodeProviderCatalogUnavailable || !body.Retryable {
		t.Fatalf("unavailable response = %d %s", response.Code, response.Body.String())
	}
	if body.Details["provider_id"] != testProviderID || body.Details["model"] != "model-x" || len(body.Details) != 2 {
		t.Fatalf("catalog identity = %+v", body.Details)
	}
	if strings.Contains(response.Body.String(), "private provider diagnostic") {
		t.Fatal("provider diagnostic leaked into response")
	}
	after, err := server.Admin.Project.Verification.LLMService.Policy.Overlay(llm.SettingsScopeGlobal, "")
	testutil.FailErr(t, "read policy after rejection", err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed validation changed policy")
	}
	probe := server.Routes.HarnessProviders.probeProviderConversation(t.Context(), harnessProviderProbeRequest{Provider: testProviderID, Model: "model-x", Role: llm.PolicySlotCoordinator})
	if probe["code"] != wire.ApiErrorCodeProviderCatalogUnavailable || probe["failure_kind"] != "provider" || probe["retryable"] != true {
		t.Fatalf("preflight failure = %+v", probe)
	}
	sessionRequest := httptest.NewRequest(http.MethodPost, base+"/v1/sessions", strings.NewReader(`{"project_id":"9d56788d-fdaf-42b8-b725-89d9d278dfcf","posture":"build","provider_id":"prov-a","model":"model-x"}`))
	sessionRequest.Header.Set("Authorization", "Bearer "+TestAPIToken)
	sessionRequest.Header.Set("Content-Type", "application/json")
	sessionResponse := httptest.NewRecorder()
	server.ServeHTTP(sessionResponse, sessionRequest)
	testutil.FailErr(t, "decode session assignment response", json.Unmarshal(sessionResponse.Body.Bytes(), &body))
	if sessionResponse.Code != http.StatusServiceUnavailable || body.Code != wire.ApiErrorCodeProviderCatalogUnavailable {
		t.Fatalf("session assignment = %d %s", sessionResponse.Code, sessionResponse.Body.String())
	}
	ready.Store(true)
	server.Admin.Project.Verification.LLMService.Registry.RefreshDiscovery(testProviderID)
	if response = apply("model-x"); response.Code != http.StatusOK {
		t.Fatalf("recovered assignment = %d %s", response.Code, response.Body.String())
	}
	response = apply("absent-model")
	testutil.FailErr(t, "decode absent model response", json.Unmarshal(response.Body.Bytes(), &body))
	if response.Code != http.StatusBadRequest || body.Code != wire.ApiErrorCodeInvalidRequest {
		t.Fatalf("authoritative absence = %d %s", response.Code, response.Body.String())
	}
	if completions.Load() != 0 {
		t.Fatalf("assignment made %d completion requests", completions.Load())
	}
}
