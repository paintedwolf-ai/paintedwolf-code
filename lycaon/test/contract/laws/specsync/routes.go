package specsync

import (
	"slices"
	"strings"

	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
	"github.com/lycaon/lycaon/test/contract/laws"
)

var routeRules = []laws.Rule{
	{Name: "routes-match-the-server-both-ways", Anchors: []laws.Anchor{openAPI, routeDoor}, Check: routesMatchTheServer},
	{Name: "routes-have-operation-ids", Anchors: []laws.Anchor{openAPI}, Check: routesHaveOperationIDs},
	{Name: "bearer-security-scheme", Anchors: []laws.Anchor{openAPI}, Check: bearerSecurityScheme},
	{Name: "route-security", Anchors: []laws.Anchor{openAPI}, Check: routeSecurity},
	{Name: "client-methods-are-operations", Anchors: []laws.Anchor{openAPI, laws.File(clientPath)}, Check: clientMethodsAreOperations},
}

const clientPath = "lycaon-den/src/api/client.ts"

// Every server route is documented, every documented route that is not a
// stub is served, a stub is not served, and every schema reference resolves.
func routesMatchTheServer(r *laws.Run) {
	server := serverRoutes(r)
	documented := specRoutes(r)
	r.Expect(len(server), "server routes")
	serverSet, openAPISet := wirespec.RouteSet(server), wirespec.RouteSet(documented)
	for _, route := range server {
		if _, ok := openAPISet[wirespec.RouteKey(route.Method, route.Path)]; !ok {
			r.Report("server route %s %s is not documented", route.Method, route.Path)
		}
	}
	for _, route := range documented {
		_, served := serverSet[wirespec.RouteKey(route.Method, route.Path)]
		switch {
		case route.Stub && served:
			r.Report("%s %s is marked stub but the server registers it", route.Method, route.Path)
		case !route.Stub && !served:
			r.Report("%s %s (%s) has no server handler", route.Method, route.Path, route.OperationID)
		}
	}
	must(r, "resolve schema references", spec(r).ResolveSchemaRefs())
}

func routesHaveOperationIDs(r *laws.Run) {
	for _, route := range specRoutes(r) {
		if route.OperationID == "" {
			r.Report("%s %s has no operationId", route.Method, route.Path)
		}
	}
}

func bearerSecurityScheme(r *laws.Run) {
	doc, err := spec(r).Security()
	must(r, "decode security", err)
	scheme, ok := doc.Components.SecuritySchemes["bearerAuth"]
	switch {
	case !ok:
		r.Report("components.securitySchemes has no bearerAuth")
	case scheme.Type != "http" || scheme.Scheme != "bearer":
		r.Report("bearerAuth is %+v, want http bearer", scheme)
	}
	if !requiresBearer(doc.Security) {
		r.Report("the root security requirement is %+v, want bearerAuth", doc.Security)
	}
}

// /health is open; every /v1 route requires bearerAuth.
func routeSecurity(r *laws.Run) {
	doc, err := spec(r).Security()
	must(r, "decode security", err)
	for _, route := range specRoutes(r) {
		op := doc.Paths[route.Path][strings.ToLower(route.Method)]
		effective := doc.Security
		if op.Security != nil {
			effective = *op.Security
		}
		switch {
		case route.Path == "/health" && len(effective) != 0:
			r.Report("/health must declare security: [], got %+v", effective)
		case strings.HasPrefix(route.Path, "/v1/") && !requiresBearer(effective):
			r.Report("%s %s must require bearerAuth, got %+v", route.Method, route.Path, effective)
		}
	}
}

func requiresBearer(requirements []wirespec.SecurityRequirement) bool {
	return slices.ContainsFunc(requirements, func(req wirespec.SecurityRequirement) bool {
		_, ok := req["bearerAuth"]
		return ok
	})
}

// Every Den client method is named for the one OpenAPI operation it calls.
func clientMethodsAreOperations(r *laws.Run) {
	ids, err := spec(r).OperationIDs()
	must(r, "list operationIds", err)
	methods, err := wirespec.ParseTSClientMethods(readRepo(r), clientPath)
	must(r, "parse LycaonClient", err)
	r.Expect(len(methods), "LycaonClient methods")
	for _, method := range sortedNames(methods) {
		if !slices.Contains(ids, method) {
			r.Report("LycaonClient.%s is not named for an OpenAPI operationId", method)
		}
	}
}
