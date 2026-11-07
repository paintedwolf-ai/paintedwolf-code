package hostcontracts

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestLocalGenerationRepairRouteUsesHostState(t *testing.T) {
	discovery := contractfixture.NewProvADiscoveryServer(t)
	catalog := strings.Replace(contractfixture.FakeProvidersYAML(discovery.URL), "providers:\n", "providers:\n  - id: bundled\n    kind: nativegen\n    api_key_env: \"\"\n    requires_api_key: false\n    http_retry:\n      max_retries: 1\n      max_wait_ms: 1000\n      backoff_ms: [1]\n      statuses: [429]\n      wait_headers: [Retry-After]\n", 1)
	_, base := contractfixture.NewProviderTestServerWithCatalogs(t, catalog, catalog)
	for _, tc := range []struct {
		id     string
		authed bool
		status int
	}{
		{"bundled", false, http.StatusUnauthorized},
		{"missing", true, http.StatusNotFound},
		{contractfixture.TestProviderID, true, http.StatusBadRequest},
		{"bundled", true, http.StatusOK},
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/v1/providers/"+tc.id+"/repair", nil)
		testutil.FailErr(t, "create repair request", err)
		if tc.authed {
			hostapi.WithTestAuth(req)
		}
		response, err := http.DefaultClient.Do(req)
		testutil.FailErr(t, "request local repair", err)
		if response.StatusCode != tc.status {
			t.Fatalf("repair %s: status %d, want %d: %s", tc.id, response.StatusCode, tc.status, contractfixture.ReadBody(t, response))
		}
		if tc.status == http.StatusOK {
			var result wire.LocalGenerationRepairResult
			testutil.FailErr(t, "decode recovery state", json.NewDecoder(response.Body).Decode(&result))
			if result.OK || result.Status.State != "unavailable" || result.Status.RecoveryAction != "reinstall" {
				t.Fatalf("missing runtime recovery: %+v", result)
			}
		}
		testutil.FailErr(t, "close repair response", response.Body.Close())
	}
}
