package hostcontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestHostResourcesRoutes(t *testing.T) {
	t.Setenv("LYCAON_HOST_RESOURCE_API_TEST", "1")
	catalog := []byte(`version: 1
resources:
  - id: test
    family: test
    label: Test
    category: Test
    description: Test host resource.
    surfaces: [process_exec]
    realizations:
      - discover:
          environment:
            names: [LYCAON_HOST_RESOURCE_API_TEST]
        connections:
          - mode: none
`)
	deps := contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: storetest.New(t, testdbseed.DefaultProjectID)}})
	configtest.Only(t, map[config.Rel]string{config.HostResources: string(catalog)})
	configDir := t.TempDir()
	service, err := hostresources.NewService(configDir)
	testutil.FailErr(t, "new service", err)
	deps.Host.HostResources = service
	server := hostapi.NewServer(deps, nil, hostapi.TestAPIToken)

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/v1/host-resources"},
		{http.MethodPost, "/v1/host-resources/refresh"},
	} {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, contractfixture.NewAuthedRequest(route.method, route.path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s %s status=%d body=%s", route.method, route.path, recorder.Code, recorder.Body.String())
		}
		var response wire.HostResourcesResponse
		testutil.FailErr(t, "decode response", json.Unmarshal(recorder.Body.Bytes(), &response))
		if response.UserCatalogPath != hostresources.UserCatalogPath(configDir) {
			t.Fatalf("catalog path = %q", response.UserCatalogPath)
		}
		if len(response.Resources) != 1 || response.Resources[0].Status != wire.HostResourceStatusAvailable {
			t.Fatalf("response = %#v", response)
		}
	}
}
