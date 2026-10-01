package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/harnessfixture"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/version"
)

func TestHarnessContractRequiresIsolatedAuthenticatedChannel(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "harness"}[enabled], func(t *testing.T) {
			t.Setenv(configdir.EnvDev, "1")
			t.Setenv(configdir.EnvHarness, map[bool]string{false: "", true: "1"}[enabled])
			server := NewServer(requiredTestDeps(t, Dependencies{Store: sessionstore.NewMemory()}), nil, TestAPIToken)
			for _, authenticated := range []bool{false, true} {
				request := httptest.NewRequest(http.MethodGet, "/harness/contract", nil)
				if authenticated {
					request.Header.Set("Authorization", "Bearer "+TestAPIToken)
				}
				response := httptest.NewRecorder()
				server.ServeHTTP(response, request)
				want := http.StatusNotFound
				if enabled && configdir.IsDevelopmentBuild() {
					want = http.StatusUnauthorized
					if authenticated {
						want = http.StatusOK
					}
				}
				if response.Code != want {
					t.Fatalf("contract status = %d, want %d", response.Code, want)
				}
				if want == http.StatusOK {
					var result struct {
						Version  string         `json:"application_version"`
						Profile  string         `json:"profile"`
						Contract map[string]any `json:"contract"`
					}
					testutil.FailErr(t, "decode contract", json.Unmarshal(response.Body.Bytes(), &result))
					var expected map[string]any
					testutil.FailErr(t, "decode compiled contract", json.Unmarshal(harnessfixture.Contract(), &expected))
					if result.Version != version.Version || result.Profile != "development-harness" || !reflect.DeepEqual(result.Contract, expected) {
						t.Fatalf("contract = %+v", result)
					}
				}
			}
		})
	}
}
