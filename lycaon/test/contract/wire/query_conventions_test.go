package contract

import (
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

// Canonical query parameter names for project/session scoping on the HTTP API.
const (
	canonicalProjectQuery = "project_id"
	canonicalSessionQuery = "session_id"
)

func TestOpenAPIQueryParamsFollowConventions(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	routes, err := wirespec.LoadOpenAPIRouteQueries(root)
	contractcheck.FailErr(t, "loadOpenAPIRouteQueries failed", err)

	projectScopedPaths := map[string]bool{
		"/v1/workers": true,
		"/v1/events":  true,
	}
	costPath := "/v1/cost/summary"

	for _, route := range routes {
		if route.Method != "GET" {
			continue
		}
		t.Run(route.Method+" "+route.Path, func(t *testing.T) {
			t.Parallel()
			for _, name := range route.QueryParams {
				if _, forbidden := wirespec.ForbiddenOpenAPIQueryNames[name]; forbidden {
					t.Fatalf("forbidden query param %q on %s (use %s / %s)", name, route.Path, canonicalProjectQuery, canonicalSessionQuery)
				}
				if route.Path == costPath && name == canonicalProjectQuery {
					// cost allows project_id OR session_id
					continue
				}
			}

			if projectScopedPaths[route.Path] {
				if !contractcheck.ContainsString(route.QueryParams, canonicalProjectQuery) {
					t.Fatalf("%s must declare required query param %q, got %v", route.Path, canonicalProjectQuery, route.QueryParams)
				}
				for _, name := range route.QueryParams {
					if strings.HasPrefix(name, "project") && name != canonicalProjectQuery {
						t.Fatalf("%s: unexpected project-related query param %q", route.Path, name)
					}
				}
			}

			if route.Path == costPath {
				hasSession := contractcheck.ContainsString(route.QueryParams, canonicalSessionQuery)
				hasProject := contractcheck.ContainsString(route.QueryParams, canonicalProjectQuery)
				if !hasSession || !hasProject {
					t.Fatalf("%s must allow %q and %q, got %v", costPath, canonicalSessionQuery, canonicalProjectQuery, route.QueryParams)
				}
			}

			if route.OperationID == "getProjectCostReport" {
				if !contractcheck.ContainsString(route.QueryParams, "q") {
					t.Fatalf("getProjectCostReport must declare canonical query param %q, got %v", "q", route.QueryParams)
				}
				if contractcheck.ContainsString(route.QueryParams, "search") {
					t.Fatalf("getProjectCostReport must not declare forbidden query param %q", "search")
				}
			}
			if route.OperationID == "searchSourceView" {
				if !contractcheck.ContainsString(route.QueryParams, "q") {
					t.Fatalf("searchSourceView must declare canonical query param %q, got %v", "q", route.QueryParams)
				}
			}
		})
	}
}

func TestAPIHandlersDoNotUseQueryParamHelper(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	apiDir := filepath.Join(root, "lycaon", "internal", "api")
	err := contractcheck.WalkFiles(apiDir, map[string]struct{}{".go": {}}, true, func(path string, data []byte) error {
		if strings.Contains(string(data), "queryParam(") {
			t.Errorf("%s: queryParam() multi-alias helper is forbidden; use RequireProjectIDQuery or explicit canonical keys", path)
		}
		return nil
	})
	contractcheck.FailErr(t, "scan API query helpers", err)
}

func TestSearchEndpointsEnforceCanonicalQParameter(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	routes, err := wirespec.LoadOpenAPIRouteQueries(root)
	contractcheck.FailErr(t, "loadOpenAPIRouteQueries failed", err)

	qEndpoints := map[string]bool{
		"getProjectCostReport":         true,
		"searchSourceView":             true,
		"searchProjectSource":          true,
		"searchProjectSymbols":         true,
		"searchChatContent":            true,
		"searchCommandReceiptDocument": true,
	}

	for _, route := range routes {
		if !qEndpoints[route.OperationID] {
			continue
		}
		t.Run(route.OperationID, func(t *testing.T) {
			t.Parallel()
			if !contractcheck.ContainsString(route.QueryParams, "q") {
				t.Fatalf("%s must declare canonical query param 'q', got %v", route.OperationID, route.QueryParams)
			}
			for _, bad := range []string{"query", "search", "term"} {
				if contractcheck.ContainsString(route.QueryParams, bad) {
					t.Fatalf("%s must not declare forbidden query param %q, got %v", route.OperationID, bad, route.QueryParams)
				}
			}
		})
	}

	apiDir := filepath.Join(root, "lycaon", "internal", "api")
	err = contractcheck.WalkFiles(apiDir, map[string]struct{}{".go": {}}, true, func(path string, data []byte) error {
		base := filepath.Base(path)
		if strings.HasSuffix(base, "_test.go") {
			return nil
		}
		content := string(data)
		if base == "cost.go" {
			if strings.Contains(content, `Get("search")`) || strings.Contains(content, `Get("query")`) {
				t.Errorf("%s: must not read forbidden 'search' or 'query' query params", path)
			}
		}
		if base == "source_view_search.go" {
			if strings.Contains(content, `SingleQueryValue(r, "query")`) || strings.Contains(content, `SingleQueryValue(r, "search")`) {
				t.Errorf("%s: must not read forbidden 'search' or 'query' query params", path)
			}
		}
		if base == "chat_content.go" {
			if strings.Contains(content, `Get("query")`) || strings.Contains(content, `Get("search")`) {
				t.Errorf("%s: must not read forbidden 'query' or 'search' query params", path)
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "scan API search query handlers", err)
}
