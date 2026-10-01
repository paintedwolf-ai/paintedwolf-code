package contract

import (
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

func TestServerRoutesDocumentedInOpenAPI(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	serverRoutes, err := wirespec.LoadServerRoutes(root)
	contractcheck.FailErr(t, "load server routes from internal/api/server.go", err)
	openAPIRoutes, err := wirespec.LoadOpenAPIRoutes(root)
	contractcheck.FailErr(t, "load OpenAPI routes from docs/openapi.yaml", err)
	openAPI := wirespec.RouteSet(openAPIRoutes)

	var missing []string
	for _, route := range serverRoutes {
		key := wirespec.RouteKey(route.Method, route.Path)
		if _, ok := openAPI[key]; !ok {
			missing = append(missing, route.Method+" "+route.Path)
		}
	}
	contractcheck.FailViolations(t, "server routes missing from docs/openapi.yaml", missing)
}

func TestOpenAPINonStubRoutesImplementedOnServer(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	serverRoutes, err := wirespec.LoadServerRoutes(root)
	contractcheck.FailErr(t, "load server routes from internal/api/server.go", err)
	openAPIRoutes, err := wirespec.LoadOpenAPIRoutes(root)
	contractcheck.FailErr(t, "load OpenAPI routes from docs/openapi.yaml", err)
	server := wirespec.RouteSet(serverRoutes)

	var missing []string
	for _, route := range openAPIRoutes {
		if route.Stub {
			continue
		}
		key := wirespec.RouteKey(route.Method, route.Path)
		if _, ok := server[key]; !ok {
			missing = append(missing, route.Method+" "+route.Path+" (operationId="+route.OperationID+")")
		}
	}
	contractcheck.FailViolations(t, "OpenAPI non-stub routes missing from internal/api/server.go", missing)
}

func TestOpenAPIStubRoutesNotOnServer(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	serverRoutes, err := wirespec.LoadServerRoutes(root)
	contractcheck.FailErr(t, "load server routes from internal/api/server.go", err)
	openAPIRoutes, err := wirespec.LoadOpenAPIRoutes(root)
	contractcheck.FailErr(t, "load OpenAPI routes from docs/openapi.yaml", err)
	server := wirespec.RouteSet(serverRoutes)

	var violations []string
	for _, route := range openAPIRoutes {
		if !route.Stub {
			continue
		}
		key := wirespec.RouteKey(route.Method, route.Path)
		if _, ok := server[key]; ok {
			violations = append(violations, route.Method+" "+route.Path+" (operationId="+route.OperationID+") — remove x-paintedwolf-status: stub or unregister route")
		}
	}
	contractcheck.FailViolations(t, "OpenAPI stub routes incorrectly registered on server", violations)
}

func TestServerRoutesNotMarkedStubInOpenAPI(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	serverRoutes, err := wirespec.LoadServerRoutes(root)
	contractcheck.FailErr(t, "load server routes from internal/api/server.go", err)
	openAPIRoutes, err := wirespec.LoadOpenAPIRoutes(root)
	contractcheck.FailErr(t, "load OpenAPI routes from docs/openapi.yaml", err)
	openAPI := wirespec.RouteSet(openAPIRoutes)

	var violations []string
	for _, route := range serverRoutes {
		key := wirespec.RouteKey(route.Method, route.Path)
		doc, ok := openAPI[key]
		if !ok {
			continue
		}
		if doc.Stub {
			violations = append(violations, route.Method+" "+route.Path+" must not be marked x-paintedwolf-status: stub")
		}
	}
	contractcheck.FailViolations(t, "implemented server routes marked stub in OpenAPI", violations)
}

func TestOpenAPISchemaRefsResolve(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	contractcheck.FailErr(t, "validate OpenAPI schema $ref targets", wirespec.ValidateOpenAPISchemaRefs(root))
}
