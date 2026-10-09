package searchcontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSearchExplicitDependenciesCoverCodeAndDeclarations(t *testing.T) {
	srv, p := contractfixture.SymbolSearchProject(t, map[string]string{
		".cache/pkg/cache.go":               "package cache\nfunc DependencyTarget() {}\n",
		".claude/worktrees/nested/.git":     "gitdir: ../../../.git/worktrees/nested\n",
		".claude/worktrees/nested/other.go": "package hidden\nfunc DependencyTarget() {}\n",
		".secret.go":                        "package secret\nfunc DependencyTarget() {}\n",
		"main.go":                           "package main\nfunc SourceTarget() {}\n",
		"node_modules/pkg/dependency.go":    "package dependency\nfunc DependencyTarget() {}\n",
		"nested/.git":                       "gitdir: ../.git/worktrees/nested\n",
		"nested/other.go":                   "package nested\nfunc DependencyTarget() {}\n",
	})
	for _, kind := range []string{"code", "symbol", "file"} {
		term := "DependencyTarget"
		if kind == "file" {
			term = "dependency.go"
		}
		resp := contractfixture.SettledSearch(t, srv, p, map[string]any{"query": "kind:" + kind + " " + term, "include_dependencies": true})
		if len(resp.Hits) == 0 {
			t.Fatalf("explicit dependency %s search returned no hits", kind)
		}
		if kind != "file" && len(resp.Hits) != 4 {
			t.Fatalf("explicit %s search missed boundary cohorts: %+v", kind, resp.Hits)
		}
		for _, hit := range resp.Hits {
			if !strings.HasPrefix(hit.Path, "node_modules/") && !strings.HasPrefix(hit.Path, "nested/") && !strings.HasPrefix(hit.Path, ".cache/") && !strings.HasPrefix(hit.Path, ".claude/worktrees/") {
				t.Fatalf("unexpected dependency %s hit: %+v", kind, hit)
			}
		}
	}
	for _, kind := range []string{"code", "symbol"} {
		resp := contractfixture.SettledSearch(t, srv, p, map[string]any{"query": "kind:" + kind + " DependencyTarget"})
		if len(resp.Hits) != 0 {
			t.Fatalf("default %s admitted dependency declarations: %+v", kind, resp.Hits)
		}
	}
	for _, kind := range []string{"code", "symbol", "file"} {
		query := "kind:" + kind + " path:node_modules/pkg/dependency.go"
		if kind != "file" {
			query += " DependencyTarget"
		}
		scoped := contractfixture.SettledSearch(t, srv, p, map[string]any{"query": query})
		if len(scoped.Hits) == 0 {
			t.Fatalf("literal boundary path %s produced no on-demand hits", kind)
		}
		for _, hit := range scoped.Hits {
			if hit.Path != "node_modules/pkg/dependency.go" {
				t.Fatalf("literal path expanded another cohort: %+v", hit)
			}
		}
	}
	mixed := contractfixture.SettledSearch(t, srv, p, map[string]any{"query": "(kind:code path:node_modules/pkg/dependency.go DependencyTarget) OR (kind:code SourceTarget)"})
	if len(mixed.Hits) != 2 {
		t.Fatalf("mixed scope lost first-party source or expanded other cohorts: %+v", mixed.Hits)
	}
	for _, include := range []bool{false, true} {
		raw, err := json.Marshal(map[string]any{"path": "main.go", "symbol": "DependencyTarget", "root_id": p.Roots[0].ID, "include_dependencies": include})
		testutil.FailErr(t, "encode definition request", err)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, contractfixture.NewAuthedRequest(http.MethodPost, "/v1/projects/"+p.ID+"/source/definition", strings.NewReader(string(raw))))
		if rec.Code != http.StatusOK {
			t.Fatalf("definition include=%v status=%d body=%s", include, rec.Code, rec.Body.String())
		}
		var result wire.SourceDefinitionResponse
		testutil.FailErr(t, "decode definition response", json.Unmarshal(rec.Body.Bytes(), &result))
		if (len(result.Candidates) > 0) != include {
			t.Fatalf("definition include=%v candidates=%+v", include, result.Candidates)
		}
	}
}
