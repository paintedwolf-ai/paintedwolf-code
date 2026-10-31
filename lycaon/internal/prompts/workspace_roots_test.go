package prompts_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
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
	if !strings.Contains(out, "Project folder: /tmp/lycaon") {
		t.Fatalf("output = %q want single workspace line", out)
	}
	if strings.Contains(out, "@") {
		t.Fatalf("single-root output must not mention @label: %q", out)
	}
	if !strings.Contains(out, "For a path the person gives you") {
		t.Fatalf("single-root output missing absolute-path grant rule: %q", out)
	}
	if !strings.Contains(out, "`src/main.py` for files, `.` for the folder") {
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
	for _, want := range []string{"lycaon", "lycaon-den", "spans 2 folders", "@<label>", "For a path the person gives you"} {
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

func TestCommandSurfaceProjectAndOutsideFolderBehaviour(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	roots := []projectroot.RootRef{
		{ID: "r1", Label: "app", Path: "/workspace/app", IsPrimary: true},
	}
	const (
		coverage    = "These folders are the project; search indexing, history and undo cover only them."
		nonStrictLn = "Other paths ask the person first, except temporary and cache folders."
		strictLn    = "Other paths, including temporary folders, ask the person first."
	)

	for _, tc := range []struct {
		name     string
		posture  gate.Posture
		wantLine string
		omitLine string
	}{
		{
			name:     "light",
			posture:  gate.PostureLight,
			wantLine: nonStrictLn,
			omitLine: strictLn,
		},
		{
			name:     "balanced",
			posture:  gate.PostureBalanced,
			wantLine: nonStrictLn,
			omitLine: strictLn,
		},
		{
			name:     "strict",
			posture:  gate.PostureStrict,
			wantLine: strictLn,
			omitLine: nonStrictLn,
		},
		{
			name:     "unset posture fails closed to strict",
			posture:  "",
			wantLine: strictLn,
			omitLine: nonStrictLn,
		},
		{
			name:     "unknown posture fails closed to strict",
			posture:  gate.Posture("unknown"),
			wantLine: strictLn,
			omitLine: nonStrictLn,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vars := prompts.AgentPromptSurfaceTemplateVars(prompts.AgentPromptSurface{
				SessionScratchDir: "/scratch/s1",
				Posture:           tc.posture,
			})
			vars["tool_profile"] = "implement"
			prompts.MergeWorkspaceRootsVars(vars, roots, "/workspace/app")
			out, err := engine.Render(context.Background(), "partials/command-surface.md", vars)
			testutil.FailErr(t, "render command-surface", err)
			if !strings.Contains(out, coverage) {
				t.Fatalf("missing project coverage sentence:\n%s", out)
			}
			if !strings.Contains(out, tc.wantLine) {
				t.Fatalf("missing outside-folder sentence %q:\n%s", tc.wantLine, out)
			}
			if strings.Contains(out, tc.omitLine) {
				t.Fatalf("unexpected outside-folder sentence %q:\n%s", tc.omitLine, out)
			}
			for _, term := range []string{"posture", "gate", "lease", "floor"} {
				if strings.Contains(strings.ToLower(out), term) {
					t.Fatalf("command-surface leaked internal term %q:\n%s", term, out)
				}
			}
		})
	}

	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "unset template fact fails closed to strict",
			mutate: func(vars map[string]any) {
				delete(vars, "reviews_confined_writes")
			},
		},
		{
			name: "unknown template fact fails closed to strict",
			mutate: func(vars map[string]any) {
				vars["reviews_confined_writes"] = "unknown"
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vars := map[string]any{"tool_profile": "implement", "session_scratch_dir": "/scratch/s1"}
			prompts.MergeWorkspaceRootsVars(vars, roots, "/workspace/app")
			tc.mutate(vars)
			out, err := engine.Render(context.Background(), "partials/command-surface.md", vars)
			testutil.FailErr(t, "render command-surface with "+tc.name, err)
			if !strings.Contains(out, strictLn) || strings.Contains(out, nonStrictLn) {
				t.Fatalf("expected strict copy for %s:\n%s", tc.name, out)
			}
		})
	}
}

func TestPostureChangedNoticeCopy(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	const strictNotice = "The person now wants to approve writes outside the project, including temporary folders."
	for _, p := range []gate.Posture{gate.PostureStrict, "", gate.Posture("unknown")} {
		strictOut, err := engine.RenderKick(context.Background(), "coordinator-posture-changed", prompts.PostureChangedKickData(p))
		testutil.FailErr(t, "render strict posture notice", err)
		if strings.TrimSpace(strictOut) != strictNotice {
			t.Fatalf("posture %q notice = %q", p, strictOut)
		}
	}
	for _, raw := range []map[string]any{{}, {"reviews_confined_writes": "unknown"}} {
		out, err := engine.RenderKick(context.Background(), "coordinator-posture-changed", raw)
		testutil.FailErr(t, "render fallback posture notice", err)
		if strings.TrimSpace(out) != strictNotice {
			t.Fatalf("fallback notice (%v) = %q", raw, out)
		}
	}
	for _, p := range []gate.Posture{gate.PostureBalanced, gate.PostureLight} {
		out, err := engine.RenderKick(context.Background(), "coordinator-posture-changed", prompts.PostureChangedKickData(p))
		testutil.FailErr(t, "render non-strict posture notice", err)
		if strings.TrimSpace(out) != "The person now allows writes to temporary and cache folders without asking; other paths outside the project still ask first." {
			t.Fatalf("%s notice = %q", p, out)
		}
		for _, term := range []string{"posture", "gate", "lease", "grant", "floor", "owner", "oar"} {
			if strings.Contains(strings.ToLower(out), term) || strings.Contains(strings.ToLower(strictNotice), term) {
				t.Fatalf("posture notice leaked internal term %q", term)
			}
		}
	}
	extHiddenOut, err := engine.RenderKick(context.Background(), "coordinator-posture-changed", map[string]any{
		"reviews_confined_writes":  true,
		"command_external_changed": true,
		"command_external_hidden":  true,
	})
	testutil.FailErr(t, "render posture+external hidden notice", err)
	if !strings.Contains(extHiddenOut, "The `external` selector is now hidden on `command`.") {
		t.Fatalf("expected external hidden sentence, got %q", extHiddenOut)
	}
}

func TestCommandSurfaceProjectCoverageAndPostureCopy(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	roots := []projectroot.RootRef{
		{ID: "r1", Label: "app", Path: "/workspace/app", IsPrimary: true},
	}
	const (
		coverage    = "These folders are the project; search indexing, history and undo cover only them."
		strictLn    = "Other paths, including temporary folders, ask the person first."
		nonStrictLn = "Other paths ask the person first, except temporary and cache folders."
	)
	for _, tc := range []struct {
		name     string
		posture  gate.Posture
		wantLine string
		omitLine string
	}{
		{
			name:     "strict asks for temporary folders too",
			posture:  gate.PostureStrict,
			wantLine: strictLn,
			omitLine: nonStrictLn,
		},
		{
			name:     "balanced exempts temporary and cache folders",
			posture:  gate.PostureBalanced,
			wantLine: nonStrictLn,
			omitLine: strictLn,
		},
		{
			name:     "light exempts temporary and cache folders",
			posture:  gate.PostureLight,
			wantLine: nonStrictLn,
			omitLine: strictLn,
		},
		{
			name:     "unset posture fails closed to strict",
			posture:  "",
			wantLine: strictLn,
			omitLine: nonStrictLn,
		},
		{
			name:     "unknown posture fails closed to strict",
			posture:  gate.Posture("unknown"),
			wantLine: strictLn,
			omitLine: nonStrictLn,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vars := prompts.AgentPromptSurfaceTemplateVars(prompts.AgentPromptSurface{
				SessionScratchDir: "/scratch/s1",
				Posture:           tc.posture,
			})
			vars["tool_profile"] = "implement"
			prompts.MergeWorkspaceRootsVars(vars, roots, "/workspace/app")
			out, err := engine.Render(context.Background(), "partials/command-surface.md", vars)
			testutil.FailErr(t, "render command-surface", err)
			if !strings.Contains(out, coverage) {
				t.Fatalf("missing project coverage sentence:\n%s", out)
			}
			if !strings.Contains(out, tc.wantLine) {
				t.Fatalf("missing outside-folder sentence %q:\n%s", tc.wantLine, out)
			}
			if strings.Contains(out, tc.omitLine) {
				t.Fatalf("unexpected outside-folder sentence %q:\n%s", tc.omitLine, out)
			}
			for _, term := range []string{"posture", "gate", "lease", "grant", "floor", "owner", "oar"} {
				if strings.Contains(strings.ToLower(out), term) {
					t.Fatalf("command-surface leaked internal term %q:\n%s", term, out)
				}
			}
		})
	}
}
