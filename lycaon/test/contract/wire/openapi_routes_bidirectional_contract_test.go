package contract

import (
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

// anchorOperations are operations both loaders must find; an empty or partial
// parse fails here instead of passing on nothing.
var anchorOperations = []string{"listWorkflowTemplates", "composeWorkflowFromTemplate", "persistWorkflow"}

// TestOpenAPIRoutesBidirectionalParity checks server and OpenAPI routes in both
// directions, stub markers, and schema $ref targets.
func TestOpenAPIRoutesBidirectionalParity(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	serverRoutes, err := wirespec.LoadServerRoutes(root)
	contractcheck.FailErr(t, "load server routes from internal/api/server.go", err)
	openAPIRoutes, err := wirespec.LoadOpenAPIRoutes(root)
	contractcheck.FailErr(t, "load OpenAPI routes from docs/openapi.yaml", err)

	server := wirespec.RouteSet(serverRoutes)
	openAPI := wirespec.RouteSet(openAPIRoutes)

	var violations []string
	loaded := map[string]map[string]bool{"OpenAPI": {}, "the server": {}}
	for _, route := range openAPIRoutes {
		loaded["OpenAPI"][route.OperationID] = true
	}
	for _, route := range serverRoutes {
		loaded["the server"][route.OperationID] = true
	}
	for _, operation := range anchorOperations {
		for source, operations := range loaded {
			if !operations[operation] {
				violations = append(violations, "anchor operation not loaded from "+source+": "+operation)
			}
		}
	}

	for _, route := range serverRoutes {
		key := wirespec.RouteKey(route.Method, route.Path)
		if _, ok := openAPI[key]; !ok {
			violations = append(violations, "server→openapi: undocumented "+route.Method+" "+route.Path)
		}
	}

	for _, route := range openAPIRoutes {
		key := wirespec.RouteKey(route.Method, route.Path)
		if route.Stub {
			if _, ok := server[key]; ok {
				violations = append(violations, "openapi stub registered on server: "+route.Method+" "+route.Path)
			}
			continue
		}
		if _, ok := server[key]; !ok {
			violations = append(violations, "openapi→server: missing handler "+route.Method+" "+route.Path+" (operationId="+route.OperationID+")")
			continue
		}
		if doc, ok := openAPI[key]; ok && doc.Stub {
			violations = append(violations, "server route marked stub in OpenAPI: "+route.Method+" "+route.Path)
		}
	}

	contractcheck.FailErr(t, "validate OpenAPI schema $ref targets", wirespec.ValidateOpenAPISchemaRefs(root))
	contractcheck.FailViolations(t, "OpenAPI ↔ server route bidirectional parity violations", violations)
}
