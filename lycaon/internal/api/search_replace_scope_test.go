package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReplacementRejectsFileOnlyQueries(t *testing.T) {
	registry := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), registry, t.TempDir())
	testutil.FailErr(t, "create project", err)
	server := &Server{projectRegistry: registry}
	for _, endpoint := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"preview", server.handlePreviewSearchReplacement},
		{"apply", server.handleApplySearchReplacement},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			operation := ""
			if endpoint.name == "apply" {
				operation = `,"operation_id":"9c92fa82-95bf-41aa-9f7b-7be6f152f0db"`
			}
			body := fmt.Sprintf(`{"query":"kind:file foo","origin_project_id":%q%s}`, p.ID, operation)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			endpoint.handler(rec, req)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "content search query") {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestReplaceRootsIntersectsQueryAndOrigin(t *testing.T) {
	registry := project.NewMemoryRegistry()
	a, err := project.CreateWithRoot(t.Context(), registry, t.TempDir())
	testutil.FailErr(t, "create origin project", err)
	b, err := project.CreateWithRoot(t.Context(), registry, t.TempDir())
	testutil.FailErr(t, "create other project", err)
	aRoot := search.CodeRoot{ProjectID: a.ID, RootID: a.Roots[0].ID, Path: a.Roots[0].Path}
	bRoot := search.CodeRoot{ProjectID: b.ID, RootID: b.Roots[0].ID, Path: b.Roots[0].Path}
	for _, tc := range []struct {
		name   string
		origin string
		roots  []search.CodeRoot
		want   int
	}{
		{"origin narrows global query", a.ID, []search.CodeRoot{aRoot, bRoot}, 1},
		{"other project does not become origin", a.ID, []search.CodeRoot{bRoot}, 0},
		{"global preview retains roots", "", []search.CodeRoot{aRoot, bRoot}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			roots := replaceRoots(tc.origin, tc.roots)
			if len(roots) != tc.want {
				t.Fatalf("roots = %+v, want %d", roots, tc.want)
			}
			for _, root := range roots {
				if root.RootID == "" || (tc.origin != "" && root.ProjectID != tc.origin) {
					t.Fatalf("unresolved or out-of-scope root = %+v", root)
				}
			}
		})
	}
}
