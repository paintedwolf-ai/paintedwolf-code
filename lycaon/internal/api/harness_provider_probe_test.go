package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/llm"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
)

func TestHarnessProviderProbeRequiresHarnessAuthenticationAndLiveOptIn(t *testing.T) {
	for _, path := range []string{"/harness/llm/provider-probe", "/harness/llm/provider-tools"} {
		for _, harness := range []bool{false, true} {
			t.Run(map[bool]string{false: "absent", true: "enabled"}[harness], func(t *testing.T) {
				t.Setenv(configdir.EnvDev, "1")
				if harness {
					t.Setenv(configdir.EnvHarness, "1")
				} else {
					t.Setenv(configdir.EnvHarness, "")
				}
				server := NewServer(requiredTestDeps(t, Dependencies{Store: sessionstore.NewMemory()}), nil, TestAPIToken)
				for _, test := range []struct {
					auth, body string
					want       int
				}{
					{"", `{"allow_live":true,"provider":"fixture","model":"fixture"}`, http.StatusUnauthorized},
					{"Bearer " + TestAPIToken, `{"provider":"fixture","model":"fixture"}`, http.StatusBadRequest},
					{"Bearer " + TestAPIToken, `{"allow_live":true,"provider":"fixture","model":"fixture","role":"invalid"}`, http.StatusBadRequest},
				} {
					request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(test.body))
					request.Header.Set("Authorization", test.auth)
					request.Header.Set("Content-Type", "application/json")
					response := httptest.NewRecorder()
					server.ServeHTTP(response, request)
					want := test.want
					if !harness {
						want = http.StatusNotFound
					}
					if response.Code != want {
						t.Fatalf("probe status = %d, want %d: %s", response.Code, want, response.Body.String())
					}
				}
			})
		}
	}

}

func TestHarnessProviderProbeDoesNotBypassUnsupportedRole(t *testing.T) {
	discovery := newProvADiscoveryServer(t)
	catalog := strings.ReplaceAll(fakeProvidersYAML(discovery.URL), "tools: {state: supported}", "tools: {state: unsupported}")
	server, _ := newProviderTestServerWithCatalogs(t, catalog, catalog)
	result := server.probeProviderConversation(t.Context(), harnessProviderProbeRequest{
		Provider: testProviderID, Model: "model-x", Role: llm.PolicySlotCoordinator,
	})
	if result["accepted"] != false || result["code"] != "model_assignment_rejected" || result["failure_kind"] != "application" || result["retryable"] != false {
		t.Fatalf("unverified role reached provider execution: %+v", result)
	}
	verification, err := server.llmSvc.Registry.CheckToolCompatibility(t.Context(), testProviderID, "model-x")
	if err == nil || len(verification.Stages) != 0 {
		t.Fatal("reference check bypassed role assignment")
	}
	if len(result["stages"].([]llm.ConversationProbe)) != 0 {
		t.Fatalf("provider requests preceded role validation: %+v", result)
	}
}
