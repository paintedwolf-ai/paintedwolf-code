package api

import (
	"encoding/json"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestPatchProjectRootLabelRename(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	reg := project.NewMemoryRegistry()
	dir := t.TempDir()
	p, err := reg.Create(t.Context(), project.CreateParams{
		Name:  "roots",
		Roots: []project.AttachRootParams{{Path: dir}},
	})
	testutil.FailErr(t, "create", err)
	rootID := p.Roots[0].ID

	srv := NewServer(requiredTestDeps(t, Dependencies{Store: sessionstore.NewMemory(), Projects: reg}), nil, TestAPIToken)

	req := newAuthedRequest(
		http.MethodPatch,
		"/v1/projects/"+p.ID+"/roots/"+rootID,
		strings.NewReader(`{"label":"backend"}`),
	)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("rename status=%d body=%s", w.Code, w.Body.String())
	}
	var out wire.Project
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		testutil.FailErr(t, "decode", err)
	}
	if len(out.Roots) != 1 || out.Roots[0].Label != "backend" {
		t.Fatalf("roots = %+v, want label backend", out.Roots)
	}

	clear := newAuthedRequest(
		http.MethodPatch,
		"/v1/projects/"+p.ID+"/roots/"+rootID,
		strings.NewReader(`{"label":""}`),
	)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, clear)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("clear status=%d, want 400 body=%s", w.Code, w.Body.String())
	}
	kept, err := reg.Get(t.Context(), p.ID)
	testutil.FailErr(t, "get", err)
	if kept.Roots[0].Label != "backend" {
		t.Fatalf("empty label cleared previous: %q", kept.Roots[0].Label)
	}
}

func TestSearchProjectNamesUsePrimaryRoot(t *testing.T) {
	p := project.Project{
		ID: "project",
		Roots: []project.Root{
			{Label: "secondary"},
			{Label: "primary", IsPrimary: true},
		},
	}
	if got := searchProjectSlug(p); got != "primary" {
		t.Fatalf("slug = %q, want primary", got)
	}
	if got := searchProjectDisplayName(&p); got != "primary" {
		t.Fatalf("display name = %q, want primary", got)
	}
}
