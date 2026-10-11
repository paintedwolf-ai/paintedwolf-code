package survey_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/survey"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func surveyRepoBoundary(t *testing.T) *sandbox.Boundary {
	t.Helper()
	return sandbox.NewBoundary(sandbox.Config{
		ProjectRootRequired: true,
		RejectSymlinkEscape: true,
	}, []sandbox.ToolProfile{{
		ID:    toolprofiles.DefaultToolProfileID,
		Tools: map[string]bool{"grep": true, "find": true, "list_dir": true, "survey_repo": true},
	}})
}

func surveyRepoCtx(dir string) tools.ToolContext {
	return tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID},
	}
}

func loadBundledCatalog(t *testing.T) *survey.Catalog {
	t.Helper()
	cat, err := survey.LoadCatalog(survey.CatalogDir())
	testutil.FailErr(t, "LoadCatalog", err)
	return cat
}

func surveyRepoFixture(t *testing.T) (dir string, tool *survey.RepoTool) {
	t.Helper()
	dir = t.TempDir()
	files := map[string]string{
		"main.go":           "package main\nfunc main() { _ = os.Getenv(\"HOME\") }\n",
		"internal/store.go": "package internal\n// store\n",
		"internal/api.go":   "package internal\n// GET /v1/health\n",
	}
	for rel, body := range files {
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			testutil.FailErr(t, "mkdir", err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			testutil.FailErr(t, "write", err)
		}
	}
	tool = &survey.RepoTool{
		Boundary:      surveyRepoBoundary(t),
		BaseCatalog:   loadBundledCatalog(t),
		Caps:          survey.DefaultCaps(),
		SourceCatalog: sourcecatalog.New(),
	}
	_, err := tool.SourceCatalog.Snapshot(context.Background(), "", []sourcecatalog.Root{{ID: "r1", Path: dir}})
	testutil.FailErr(t, "warm source catalog", err)
	return dir, tool
}

type repoOut struct {
	Bundle    string `json:"bundle"`
	View      string `json:"view"`
	Digest    string `json:"digest"`
	Selected  int    `json:"selected"`
	Total     int    `json:"total"`
	ProbesRun int    `json:"probes_run"`
	Snapshot  []struct {
		Path string `json:"path"`
	} `json:"snapshot"`
	Altitude        string `json:"altitude"`
	Resolution      string `json:"resolution"`
	InventoryState  string `json:"inventory_state"`
	EntriesExamined int    `json:"entries_examined"`
	Groups          int    `json:"groups"`
}

func TestSurveyRepoLayoutUsesQualifiedSecondaryRoot(t *testing.T) {
	primary := t.TempDir()
	secondary := t.TempDir()
	testutil.FailErr(t, "write primary marker", os.WriteFile(filepath.Join(primary, "primary.go"), []byte("package primary\n"), 0o644))
	testutil.FailErr(t, "write secondary marker", os.WriteFile(filepath.Join(secondary, "secondary.go"), []byte("package secondary\n"), 0o644))
	catalog := sourcecatalog.New()
	roots := []sourcecatalog.Root{{ID: "primary", Path: primary}, {ID: "secondary", Path: secondary}}
	_, err := catalog.Snapshot(context.Background(), "project", roots)
	testutil.FailErr(t, "warm multi-root source catalog", err)
	tool := &survey.RepoTool{
		Boundary: surveyRepoBoundary(t), BaseCatalog: loadBundledCatalog(t),
		Caps: survey.DefaultCaps(), SourceCatalog: catalog,
	}
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: "project",
			Agent: toolprofiles.DefaultToolProfileID},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{
			{ID: "primary", Label: "primary", Path: primary, IsPrimary: true},
			{ID: "secondary", Label: "secondary", Path: secondary},
		},
			ActiveRootID: "primary"},
	}
	out, err := tool.Run(context.Background(), map[string]any{
		"bundle": "layout_overview", "path": "@secondary",
	}, tctx)
	testutil.FailErr(t, "survey secondary root", err)
	resp := decodeRepoOut(t, out)
	if len(resp.Snapshot) != 1 || resp.Snapshot[0].Path != "@secondary/secondary.go" {
		t.Fatalf("secondary snapshot = %+v, want only qualified secondary marker", resp.Snapshot)
	}
}

func decodeRepoOut(t *testing.T, out string) repoOut {
	t.Helper()
	var resp repoOut
	testutil.FailErr(t, "decode survey_repo out", json.Unmarshal([]byte(out), &resp))
	return resp
}

func TestSurveyRepoOrientationEnvelope(t *testing.T) {
	dir, tool := surveyRepoFixture(t)
	out, err := tool.Run(context.Background(), map[string]any{"bundle": "ssot_drift"}, surveyRepoCtx(dir))
	testutil.FailErr(t, "survey_repo", err)
	resp := decodeRepoOut(t, out)
	if resp.Selected != 0 {
		t.Fatalf("selected = %d want 0 for orientation", resp.Selected)
	}
	if resp.Total == 0 {
		t.Fatal("expected candidates in snapshot")
	}
	if resp.View != "digest" {
		t.Fatalf("view = %q want digest", resp.View)
	}
	if resp.Digest == "" {
		t.Fatal("expected digest")
	}
	if len(resp.Snapshot) == 0 {
		t.Fatal("expected snapshot entries")
	}
	rec := evidence.BuildEvidenceRecord(dir, "survey_repo", map[string]any{"bundle": "ssot_drift", "path": "."}, out)
	if rec.Kind != "survey" || !rec.Survey {
		t.Fatalf("evidence = kind=%q survey=%v want survey/survey-grade", rec.Kind, rec.Survey)
	}
}

func TestSurveyRepoDeterministic(t *testing.T) {
	dir, tool := surveyRepoFixture(t)
	args := map[string]any{"bundle": "layout_overview", "path": "."}
	out1, err := tool.Run(context.Background(), args, surveyRepoCtx(dir))
	testutil.FailErr(t, "survey_repo run 1", err)
	out2, err := tool.Run(context.Background(), args, surveyRepoCtx(dir))
	testutil.FailErr(t, "survey_repo run 2", err)
	if out1 != out2 {
		t.Fatalf("survey_repo output not deterministic:\nfirst  %s\nsecond %s", out1, out2)
	}
}

func TestSurveyRepoLayoutReportsUsefulAltitude(t *testing.T) {
	dir, tool := surveyRepoFixture(t)
	out, err := tool.Run(context.Background(), map[string]any{"bundle": "layout_overview", "path": "."}, surveyRepoCtx(dir))
	testutil.FailErr(t, "survey_repo layout", err)
	resp := decodeRepoOut(t, out)
	if resp.Altitude != "root" || resp.Resolution != "subtree_rollups" || resp.InventoryState != "ready" {
		t.Fatalf("altitude fields = %+v", resp)
	}
	if resp.EntriesExamined == 0 || resp.Groups == 0 {
		t.Fatalf("coverage fields = %+v", resp)
	}
}

func TestSurveyRepoUnknownBundle(t *testing.T) {
	dir, tool := surveyRepoFixture(t)
	_, err := tool.Run(context.Background(), map[string]any{"bundle": "missing_bundle"}, surveyRepoCtx(dir))
	if err == nil {
		t.Fatal("expected error for unknown bundle")
	}
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject == nil || reject.Code != "SURVEY_BUNDLE_UNKNOWN" {
		t.Fatalf("err = %v want SURVEY_BUNDLE_UNKNOWN", err)
	}
}

func TestSurveyRepoBundleRequired(t *testing.T) {
	dir, tool := surveyRepoFixture(t)
	_, err := tool.Run(context.Background(), map[string]any{}, surveyRepoCtx(dir))
	if err == nil {
		t.Fatal("expected error for missing bundle")
	}
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject == nil || reject.Code != "SURVEY_BUNDLE_REQUIRED" {
		t.Fatalf("err = %v want SURVEY_BUNDLE_REQUIRED", err)
	}
}

func TestSurveyRepoProbesRun(t *testing.T) {
	dir, tool := surveyRepoFixture(t)
	out, err := tool.Run(context.Background(), map[string]any{"bundle": "api_routes"}, surveyRepoCtx(dir))
	testutil.FailErr(t, "survey_repo", err)
	resp := decodeRepoOut(t, out)
	if resp.ProbesRun == 0 {
		t.Fatal("expected probes_run > 0")
	}
	if resp.Bundle != "api_routes" {
		t.Fatalf("bundle = %q", resp.Bundle)
	}
}
