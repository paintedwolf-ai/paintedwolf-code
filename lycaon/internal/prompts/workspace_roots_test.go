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

func TestRootsChangedKickTemplateAdd(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	before := []projectroot.RootRef{{ID: "p", Label: "lycaon", Path: "/tmp/lycaon", IsPrimary: true}}
	after := append(append([]projectroot.RootRef(nil), before...), projectroot.RootRef{ID: "d", Label: "lycaon-den", Path: "/tmp/lycaon-den"})
	data := prompts.RootsChangedKickData(before, after, "")
	out, err := engine.RenderKick(context.Background(), "coordinator-roots-changed", data)
	testutil.FailErr(t, "engine.RenderKick failed", err)
	if !strings.Contains(out, "lycaon-den") || !strings.Contains(out, "2 folders") {
		t.Fatalf("kick output = %q", out)
	}
}

func TestRootsChangedKickData_BoundsRows(t *testing.T) {
	roots := make([]projectroot.RootRef, pongoplain.MaxCollectionItems+1)
	for i := range roots {
		roots[i] = projectroot.RootRef{ID: fmt.Sprintf("root-%d", i), Label: fmt.Sprintf("root-%d", i), Path: fmt.Sprintf("/tmp/root-%d", i)}
	}
	data := prompts.RootsChangedKickData(nil, roots, "")
	after := data["after"].([]map[string]any)
	if len(after) != pongoplain.MaxCollectionItems {
		t.Fatalf("after count = %d", len(after))
	}
	if got := data["after_omitted_count"].(int); got != 1 {
		t.Fatalf("after_omitted_count = %d", got)
	}
	if got := data["added_count"].(int); got != len(roots) {
		t.Fatalf("added_count = %d", got)
	}
}

func TestRootsChangedKickTemplateDetachToSingle(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	before := []projectroot.RootRef{
		{ID: "p", Label: "lycaon", Path: "/tmp/lycaon", IsPrimary: true},
		{ID: "d", Label: "lycaon-den", Path: "/tmp/lycaon-den"},
	}
	after := before[:1]
	if !prompts.RootsChangedKickNeeded(before, after) {
		t.Fatal("expected detach-to-single kick")
	}
	out, err := engine.RenderKick(context.Background(), "coordinator-roots-changed", prompts.RootsChangedKickData(before, after, ""))
	testutil.FailErr(t, "engine.RenderKick failed", err)
	if !strings.Contains(out, "single folder") {
		t.Fatalf("kick output = %q", out)
	}
}

func TestRootsChangedKickTemplateRelocation(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	before := []projectroot.RootRef{{ID: "p", Label: "lycaon", Path: "/tmp/old-spot", IsPrimary: true}}
	after := []projectroot.RootRef{{ID: "p", Label: "lycaon", Path: "/tmp/new-spot", IsPrimary: true}}
	if !prompts.RootsChangedKickNeeded(before, after) {
		t.Fatal("expected relocation kick")
	}
	out, err := engine.RenderKick(context.Background(), "coordinator-roots-changed", prompts.RootsChangedKickData(before, after, ""))
	testutil.FailErr(t, "engine.RenderKick failed", err)
	if !strings.Contains(out, "/tmp/new-spot") {
		t.Fatalf("relocation kick must name the new path, got %q", out)
	}
}

func TestRootsChangedKickTemplateDetachToZero(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	before := []projectroot.RootRef{{ID: "p", Label: "lycaon", Path: "/tmp/lycaon", IsPrimary: true}}
	if !prompts.RootsChangedKickNeeded(before, nil) {
		t.Fatal("expected no-folder kick")
	}
	out, err := engine.RenderKick(context.Background(), "coordinator-roots-changed", prompts.RootsChangedKickData(before, nil, ""))
	testutil.FailErr(t, "engine.RenderKick failed", err)
	if !strings.Contains(out, "no folder attached") {
		t.Fatalf("kick output = %q", out)
	}
}
