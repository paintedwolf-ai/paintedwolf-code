package specsync

import (
	"slices"
	"strings"

	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

var queryRules = []invariants.Invariant{
	{Name: "query-parameters-follow-conventions", Anchors: []invariants.Anchor{openAPI}, Check: queryParametersFollowConventions},
	{Name: "search-endpoints-take-q", Anchors: []invariants.Anchor{openAPI}, Check: searchEndpointsTakeQ},
}

const (
	projectQuery = "project_id"
	sessionQuery = "session_id"
	costPath     = "/v1/cost/summary"
)

// projectScopedPaths list a project through a required project_id query.
var projectScopedPaths = map[string]bool{"/v1/workers": true, "/v1/events": true}

// searchOperations take their search text as q.
var searchOperations = []string{
	"getProjectCostReport",
	"searchChatContent",
	"searchCommandReceiptDocument",
	"searchProjectSource",
	"searchSourceView",
}

func routeQueries(r *invariants.Run) []wirespec.RouteQuery {
	routes, err := spec(r).RouteQueries()
	must(r, "decode query parameters", err)
	return routes
}

func queryParametersFollowConventions(r *invariants.Run) {
	for _, route := range routeQueries(r) {
		if route.Method != "GET" {
			continue
		}
		for _, name := range route.QueryParams {
			if _, forbidden := wirespec.ForbiddenOpenAPIQueryNames[name]; forbidden {
				r.Report("%s takes query parameter %q; use %s or %s", route.Path, name, projectQuery, sessionQuery)
			}
		}
		if projectScopedPaths[route.Path] {
			if !slices.Contains(route.QueryParams, projectQuery) {
				r.Report("%s must declare query parameter %s, got %v", route.Path, projectQuery, route.QueryParams)
			}
			for _, name := range route.QueryParams {
				if strings.HasPrefix(name, "project") && name != projectQuery {
					r.Report("%s takes project-related query parameter %q", route.Path, name)
				}
			}
		}
		if route.Path == costPath && (!slices.Contains(route.QueryParams, sessionQuery) || !slices.Contains(route.QueryParams, projectQuery)) {
			r.Report("%s must accept %s and %s, got %v", costPath, sessionQuery, projectQuery, route.QueryParams)
		}
	}
}

func searchEndpointsTakeQ(r *invariants.Run) {
	found := map[string]bool{}
	for _, route := range routeQueries(r) {
		if !slices.Contains(searchOperations, route.OperationID) {
			continue
		}
		found[route.OperationID] = true
		if !slices.Contains(route.QueryParams, "q") {
			r.Report("%s must take its search text as q, got %v", route.OperationID, route.QueryParams)
		}
		for _, alias := range []string{"query", "search", "term"} {
			if slices.Contains(route.QueryParams, alias) {
				r.Report("%s takes query parameter %q; the search text is q", route.OperationID, alias)
			}
		}
	}
	for _, id := range searchOperations {
		if !found[id] {
			r.Report("search operation %s is not documented", id)
		}
	}
}
