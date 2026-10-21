package prompts_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWorkspaceRootsPartialSingleRoot(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	vars := map[string]any{}
	prompts.MergeWorkspaceRootsVars(vars, []projectroot.RootRef{
		{ID: "r1", Label: "lycaon", Path: "/tmp/lycaon", IsPrimary: true},
	}, "/tmp/lycaon")
	out, err := engine.Render(context.Background(), "partials/workspace-roots.md", vars)
	testutil.FailErr(t, "render prompt template", err)
	if !strings.Contains(out, "Workspace: /tmp/lycaon") {
		t.Fatalf("output = %q want single workspace line", out)
	}
	if strings.Contains(out, "@") {
		t.Fatalf("single-root output must not mention @label: %q", out)
	}
	if !strings.Contains(out, "User absolute paths") {
		t.Fatalf("single-root output missing absolute-path grant rule: %q", out)
	}
	if !strings.Contains(out, "`src/main.py` for files, `.` for root") {
		t.Fatalf("single-root output must distinguish file paths from folder root: %q", out)
	}
}

func TestWorkspaceRootsPartialMultiRoot(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	vars := map[string]any{}
	prompts.MergeWorkspaceRootsVars(vars, []projectroot.RootRef{
		{ID: "p", Label: "lycaon", Path: "/tmp/lycaon", IsPrimary: true},
		{ID: "d", Label: "lycaon-den", Path: "/tmp/lycaon-den", IsPrimary: false},
	}, "/tmp/lycaon")
	out, err := engine.Render(context.Background(), "partials/workspace-roots.md", vars)
	testutil.FailErr(t, "render prompt template", err)
	for _, want := range []string{"lycaon", "lycaon-den", "spans 2 folders", "@<label>", "User absolute paths"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q: %s", want, out)
		}
	}
}

func TestWorkspaceRootsPartialBoundsAndDisclosesOmissions(t *testing.T) {
	roots := make([]projectroot.RootRef, 257)
	for i := range roots {
		roots[i] = projectroot.RootRef{ID: string(rune('a' + i%26)), Label: "repo", Path: "/tmp/repo"}
	}
	vars := map[string]any{}
	prompts.MergeWorkspaceRootsVars(vars, roots, "/tmp/repo")
	rows := vars["workspace_roots"].([]map[string]any)
	if len(rows) != 256 {
		t.Fatalf("workspace rows = %d want 256", len(rows))
	}
	if got := vars["workspace_roots_omitted_count"]; got != 1 {
		t.Fatalf("omitted count = %v want 1", got)
	}
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	out, err := engine.Render(context.Background(), "partials/workspace-roots.md", vars)
	testutil.FailErr(t, "render bounded workspace roots", err)
	if !strings.Contains(out, "1 additional folders are omitted") {
		t.Fatalf("output does not disclose omission: %q", out)
	}
}

func renderRootsChanged(t *testing.T, before, after []projectroot.RootRef, active string) string {
	t.Helper()
	change := projectroot.DiffRoots(before, after)
	if change.Empty() {
		t.Fatal("fixture describes no root change")
	}
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	out, err := engine.RenderKick(context.Background(), "coordinator-roots-changed", prompts.RootsChangedKickData(change, after, active))
	testutil.FailErr(t, "render roots-changed notice", err)
	return out
}

func TestRootsChangedNoticeNamesEachChange(t *testing.T) {
	api := projectroot.RootRef{ID: "a", Label: "api", Path: "/abs/api"}
	web := projectroot.RootRef{ID: "w", Label: "web", Path: "/abs/web"}
	docs := projectroot.RootRef{ID: "d", Label: "docs", Path: "/abs/docs", IsPrimary: true}
	moved := docs
	moved.Path = "/abs/new-docs"
	renamed := web
	renamed.Label = "site"
	cases := []struct {
		name          string
		before, after []projectroot.RootRef
		want          []string
	}{
		{"added", []projectroot.RootRef{docs}, []projectroot.RootRef{docs, api}, []string{"Folders changed: added api → /abs/api.", "  • api  /abs/api"}},
		{"removed", []projectroot.RootRef{docs, web}, []projectroot.RootRef{docs}, []string{"Folders changed: removed web (was /abs/web).", "old labels no longer resolve"}},
		{"moved", []projectroot.RootRef{docs}, []projectroot.RootRef{moved}, []string{"Folders changed: moved docs → /abs/new-docs (was /abs/docs).", "start in docs (/abs/new-docs)"}},
		{"renamed", []projectroot.RootRef{docs, web}, []projectroot.RootRef{docs, renamed}, []string{"Folders changed: renamed web → site (/abs/web)."}},
		{"several", []projectroot.RootRef{docs, web}, []projectroot.RootRef{moved, api}, []string{"added api → /abs/api; moved docs → /abs/new-docs (was /abs/docs); removed web (was /abs/web)."}},
		{"last removed", []projectroot.RootRef{docs}, nil, []string{"removed docs (was /abs/docs).", "No folder is attached now."}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := renderRootsChanged(t, c.before, c.after, "")
			for _, want := range c.want {
				if !strings.Contains(out, want) {
					t.Fatalf("notice missing %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestRootsChangedNoticeNamesNewPrimary(t *testing.T) {
	a := projectroot.RootRef{ID: "a", Label: "api", Path: "/abs/api", IsPrimary: true}
	b := projectroot.RootRef{ID: "b", Label: "web", Path: "/abs/web"}
	a2, b2 := a, b
	a2.IsPrimary, b2.IsPrimary = false, true
	out := renderRootsChanged(t, []projectroot.RootRef{a, b}, []projectroot.RootRef{a2, b2}, "")
	if !strings.Contains(out, "Folders changed: primary is now web (/abs/web).") || !strings.Contains(out, "start in web (/abs/web)") {
		t.Fatalf("primary notice = %q", out)
	}
}

func TestRootsChangedKickData_BoundsRows(t *testing.T) {
	roots := make([]projectroot.RootRef, pongoplain.MaxCollectionItems+1)
	for i := range roots {
		roots[i] = projectroot.RootRef{ID: fmt.Sprintf("root-%d", i), Label: fmt.Sprintf("root-%d", i), Path: fmt.Sprintf("/tmp/root-%d", i)}
	}
	data := prompts.RootsChangedKickData(projectroot.DiffRoots(nil, roots), roots, "")
	if after := data["after"].([]map[string]any); len(after) != pongoplain.MaxCollectionItems {
		t.Fatalf("after count = %d", len(after))
	}
	if changes := data["changes"].([]map[string]any); len(changes) != pongoplain.MaxCollectionItems || data["changes_omitted"].(int) != 1 {
		t.Fatalf("changes = %d omitted = %v", len(changes), data["changes_omitted"])
	}
	if got := data["after_omitted_count"].(int); got != 1 {
		t.Fatalf("after_omitted_count = %d", got)
	}
}
