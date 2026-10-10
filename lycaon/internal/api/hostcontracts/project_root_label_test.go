package hostcontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/project"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
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

	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: sessionstore.NewMemory(), Projects: reg}}), nil, hostapi.TestAPIToken)

	req := contractfixture.NewAuthedRequest(
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

	clear := contractfixture.NewAuthedRequest(
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
