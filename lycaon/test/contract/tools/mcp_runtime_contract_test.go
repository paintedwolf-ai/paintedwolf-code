package contract

import (
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

func TestMCPRoutesNotStub(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	routes, err := wirespec.LoadOpenAPIRoutes(root)
	contractcheck.FailErr(t, "load OpenAPI routes from docs/openapi.yaml", err)
	serverRoutes, err := wirespec.LoadServerRoutes(root)
	contractcheck.FailErr(t, "load server routes from internal/api/server.go", err)
	server := wirespec.RouteSet(serverRoutes)

	want := []string{
		"GET /v1/mcp/recipes",
		"GET /v1/mcp/providers",
		"POST /v1/mcp/providers",
		"PATCH /v1/mcp/providers/{}",
		"DELETE /v1/mcp/providers/{}",
		"GET /v1/mcp/providers/{}/tools",
		"POST /v1/mcp/providers/{}/refresh",
		"POST /v1/mcp/providers/check",
	}
	for _, key := range want {
		doc, ok := wirespec.RouteSet(routes)[key]
		if !ok {
			t.Fatalf("missing openapi route %s", key)
		}
		if doc.Stub {
			t.Fatalf("route %s must not be x-paintedwolf-status: stub", key)
		}
		if _, ok := server[key]; !ok {
			t.Fatalf("route %s missing from internal/api/server.go", key)
		}
	}
}
