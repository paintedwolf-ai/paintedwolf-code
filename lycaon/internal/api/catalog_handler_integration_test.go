//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// The pack tier is the resolved catalog only — there is no disk shadow to point
// at. Listing must succeed from the catalog's captured bytes.
func TestListWorkflowsFromCatalogOnly(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	reg := project.NewMemoryRegistry()
	opened, err := project.CreateWithRoot(t.Context(), reg, t.TempDir())
	testutil.FailErr(t, "reg.Open failed", err)

	store := store.NewMemory()
	srv := NewServer(requiredTestDeps(t, Dependencies{
		Store: store, Projects: reg,
		WorkflowCatalog: workflow.ManifestResolver{
			ProjectTierApplies: func(context.Context, string) bool {
				return true
			},
		},
	}), nil, TestAPIToken)

	req := newAuthedRequest(http.MethodGet, "/v1/workflows?project_id="+opened.ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var list wire.WorkflowListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	summaries := list.Workflows
	if len(summaries) == 0 {
		t.Fatal("embed-only catalog returned no summaries")
	}
}

func TestListWorkflowsCatalogAndOverlay(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	reg := project.NewMemoryRegistry()
	opened, err := project.CreateWithRoot(t.Context(), reg, t.TempDir())
	testutil.FailErr(t, "reg.Open failed", err)
	overlayDir := filepath.Join(project.PrimaryRootPath(opened), settingsoverlay.DirName(), "workflows", "plan")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	overlay := "id: plan\nversion: 1.0.0\nname: overlay-plan\ntrigger: /plan\nrequest:\n  question: What should we plan?\nphases:\n  - id: only\n    activity_label: Test phase\n"
	if err := os.WriteFile(filepath.Join(overlayDir, "workflow.yaml"), []byte(overlay), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	store := store.NewMemory()
	srv := NewServer(requiredTestDeps(t, Dependencies{
		Store: store, Projects: reg,
		WorkflowCatalog: workflow.ManifestResolver{ProjectTierApplies: func(context.Context, string) bool { return true }},
	}), nil, TestAPIToken)

	req := newAuthedRequest(http.MethodGet, "/v1/workflows?project_id="+opened.ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var list wire.WorkflowListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	summaries := list.Workflows
	var plan *wire.WorkflowSummary
	for i := range summaries {
		if summaries[i].ID == "plan" && summaries[i].Version == "1.0.0" {
			plan = &summaries[i]
			break
		}
	}
	if plan == nil {
		t.Fatal("plan manifest missing from catalog")
	}
	if plan.Name != "overlay-plan" {
		t.Fatalf("name = %q want overlay-plan", plan.Name)
	}
	if plan.Scope != wire.WorkflowScopeProject {
		t.Fatalf("scope = %q want project", plan.Scope)
	}
	if plan.Trigger != "/plan" {
		t.Fatalf("trigger = %q", plan.Trigger)
	}
}

func TestListWorkflowsExcludesInvalidOverlayAndReportsDiagnostics(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	reg := project.NewMemoryRegistry()
	opened, err := project.CreateWithRoot(t.Context(), reg, t.TempDir())
	testutil.FailErr(t, "reg.Open failed", err)

	workflowsDir := filepath.Join(project.PrimaryRootPath(opened), settingsoverlay.DirName(), "workflows")

	attachDir := filepath.Join(workflowsDir, "implement-dispatch")
	testutil.FailErr(t, "mkdir", os.MkdirAll(attachDir, 0o755))
	attachYAML := `id: implement-dispatch
version: 1.0.0
extends: implement@1.0.0
controls:
  default_execution_mode: orchestrate
attach:
  policy: session_create
`
	testutil.FailErr(t, "write attach", os.WriteFile(filepath.Join(attachDir, "workflow.yaml"), []byte(attachYAML), 0o644))

	badExtendsDir := filepath.Join(workflowsDir, "bad-extends")
	testutil.FailErr(t, "mkdir", os.MkdirAll(badExtendsDir, 0o755))
	badExtendsYAML := `id: bad-extends
version: 1.0.0
extends: nonexistent@1.0.0
`
	testutil.FailErr(t, "write bad extends", os.WriteFile(filepath.Join(badExtendsDir, "workflow.yaml"), []byte(badExtendsYAML), 0o644))

	syntaxErrDir := filepath.Join(workflowsDir, "syntax-error")
	testutil.FailErr(t, "mkdir", os.MkdirAll(syntaxErrDir, 0o755))
	syntaxErrYAML := `id: syntax-error
version: 1.0.0
phases: [unclosed
`
	testutil.FailErr(t, "write syntax error", os.WriteFile(filepath.Join(syntaxErrDir, "workflow.yaml"), []byte(syntaxErrYAML), 0o644))

	validDir := filepath.Join(workflowsDir, "custom-valid")
	testutil.FailErr(t, "mkdir", os.MkdirAll(validDir, 0o755))
	validYAML := `id: custom-valid
version: 1.0.0
name: Custom Valid
request:
  question: What to do?
phases:
  - id: only
    activity_label: Running
    complete_when: always
    terminal: true
`
	testutil.FailErr(t, "write valid", os.WriteFile(filepath.Join(validDir, "workflow.yaml"), []byte(validYAML), 0o644))

	store := store.NewMemory()
	srv := NewServer(requiredTestDeps(t, Dependencies{
		Store: store, Projects: reg,
		WorkflowCatalog: workflow.ManifestResolver{ProjectTierApplies: func(context.Context, string) bool { return true }},
	}), nil, TestAPIToken)

	req := newAuthedRequest(http.MethodGet, "/v1/workflows?project_id="+opened.ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var list wire.WorkflowListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}

	var customValid *wire.WorkflowSummary
	for i := range list.Workflows {
		if list.Workflows[i].ID == "custom-valid" {
			customValid = &list.Workflows[i]
			break
		}
	}
	if customValid == nil {
		t.Fatal("custom-valid missing from workflows")
	}

	if len(list.Excluded) != 3 {
		t.Fatalf("excluded count = %d, want 3", len(list.Excluded))
	}
	excludedByPath := map[string]wire.ExcludedWorkflow{}
	for path, ex := range list.Excluded {
		if path != ex.Path {
			t.Fatalf("excluded workflow key = %q, want source path %q", path, ex.Path)
		}
		excludedByPath[filepath.Base(filepath.Dir(ex.Path))] = ex
	}

	if ex, ok := excludedByPath["implement-dispatch"]; !ok || len(ex.Errors) == 0 || ex.Errors[0].Code != "attach_session_create_on_overlay" {
		t.Fatalf("implement-dispatch excluded: %+v", ex)
	}
	if ex, ok := excludedByPath["bad-extends"]; !ok || len(ex.Errors) == 0 || ex.Errors[0].Code != "unknown_extends_parent" {
		t.Fatalf("bad-extends excluded: %+v", ex)
	}
	if ex, ok := excludedByPath["syntax-error"]; !ok || len(ex.Errors) == 0 || ex.Errors[0].Code != "load_error" {
		t.Fatalf("syntax-error excluded: %+v", ex)
	}
}
