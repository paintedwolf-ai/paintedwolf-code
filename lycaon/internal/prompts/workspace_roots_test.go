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
		{"added", []projectroot.RootRef{docs}, []projectroot.RootRef{docs, api}, []string{"Folders changed: added @api.", "  • @api"}},
		{"removed", []projectroot.RootRef{docs, web}, []projectroot.RootRef{docs}, []string{"Folders changed: removed @web.", "labels no longer resolve"}},
		{"moved", []projectroot.RootRef{docs}, []projectroot.RootRef{moved}, []string{"Folders changed: moved @docs.", "start in @docs (session folder)"}},
		{"renamed", []projectroot.RootRef{docs, web}, []projectroot.RootRef{docs, renamed}, []string{"Folders changed: renamed @web → @site."}},
		{"several", []projectroot.RootRef{docs, web}, []projectroot.RootRef{moved, api}, []string{"added @api; moved @docs; removed @web."}},
		{"last removed", []projectroot.RootRef{docs}, nil, []string{"removed @docs.", "No folder is attached now."}},
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
	if !strings.Contains(out, "Folders changed: primary is now @web.") || !strings.Contains(out, "start in @web (session folder)") {
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
