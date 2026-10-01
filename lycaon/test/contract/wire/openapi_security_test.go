package contract

import (
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

func TestOpenAPIHasBearerSecurityScheme(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	doc, err := wirespec.LoadOpenAPIDoc(root)
	contractcheck.FailErr(t, "loadOpenAPIDoc failed", err)
	if doc.Components.SecuritySchemes == nil {
		t.Fatal("missing components.securitySchemes")
	}
	scheme, ok := doc.Components.SecuritySchemes["bearerAuth"]
	if !ok {
		t.Fatal("missing bearerAuth security scheme")
	}
	if scheme.Type != "http" || scheme.Scheme != "bearer" {
		t.Fatalf("bearerAuth = %+v", scheme)
	}
	if !globalSecurityRequiresBearer(doc.Security) {
		t.Fatalf("root security = %+v", doc.Security)
	}
}

func TestOpenAPIRouteSecurity(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	doc, err := wirespec.LoadOpenAPIDoc(root)
	contractcheck.FailErr(t, "loadOpenAPIDoc failed", err)
	routes, err := wirespec.LoadOpenAPIRoutes(root)
	contractcheck.FailErr(t, "load OpenAPI routes from docs/openapi.yaml", err)
	for _, route := range routes {
		t.Run(route.Method+" "+route.Path, func(t *testing.T) {
			t.Parallel()
			methods := doc.Paths[route.Path]
			op, ok := methods[strings.ToLower(route.Method)]
			if !ok {
				t.Fatalf("operation not found in openapi paths")
			}
			effective := effectiveOperationSecurity(op.Security, doc.Security)
			switch {
			case route.Path == "/health":
				if len(effective) != 0 {
					t.Fatalf("health must have security: [], got %+v", effective)
				}
			case strings.HasPrefix(route.Path, "/v1/"):
				if !securityRequiresBearer(effective) {
					t.Fatalf("/v1 route must require bearerAuth, effective=%+v", effective)
				}
			}
		})
	}
}

func globalSecurityRequiresBearer(sec []wirespec.SecurityRequirement) bool {
	return securityRequiresBearer(sec)
}

func securityRequiresBearer(sec []wirespec.SecurityRequirement) bool {
	if len(sec) == 0 {
		return false
	}
	for _, req := range sec {
		if _, ok := req["bearerAuth"]; ok {
			return true
		}
	}
	return false
}

func effectiveOperationSecurity(opSec *[]wirespec.SecurityRequirement, global []wirespec.SecurityRequirement) []wirespec.SecurityRequirement {
	if opSec != nil {
		return *opSec
	}
	return global
}
