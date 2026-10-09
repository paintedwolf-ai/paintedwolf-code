package survey

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/toolscope"
)

func TestPathGlobIsOpen(t *testing.T) {
	for _, g := range []string{"", "*", "**", "**/*", "*.*"} {
		if !pathGlobIsOpen(g) {
			t.Fatalf("pathGlobIsOpen(%q) = false", g)
		}
	}
	if pathGlobIsOpen("*.go") {
		t.Fatal("*.go must narrow")
	}
	if pathGlobIsOpen("browser/**/*.js") {
		t.Fatal("nested glob must narrow")
	}
}

func TestGrepDenseRootDensenessThenReject(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.go"), "package a\n")
	mustWrite(t, filepath.Join(dir, "b.md"), "# hi\n")
	scope := toolscope.Default()
	scope.RootStructuralFileCount = 100
	tool := &GrepTool{Boundary: nativefixture.Boundary(t), Scope: &scope}
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "s-dense",
			ProjectID: "p1"},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Path: dir, IsPrimary: true}},
			ActiveRootID:       "r1",
			RepoFileCount:      200_000,
			RepoFileCountKnown: true,
			RepoTopLevel:       []string{"browser", "dom"}},
	}
	args := map[string]any{"pattern": "CVE-2026", "path": ".", "path_glob": "*"}
	out, err := tool.Run(context.Background(), args, tctx)
	testutil.FailErr(t, "first denseness grep", err)
	var resp grepResponse
	testutil.FailErr(t, "unmarshal denseness", json.Unmarshal([]byte(out), &resp))
	if resp.View != surveyViewDigest {
		t.Fatalf("view = %q want digest", resp.View)
	}
	if len(resp.Matches) != 0 {
		t.Fatalf("matches = %d want 0 (no walk)", len(resp.Matches))
	}
	if !strings.Contains(resp.Note, "path_glob") && !strings.Contains(resp.Note, "subdirectory") {
		t.Fatalf("note missing scope guidance: %q", resp.Note)
	}

	_, err = tool.Run(context.Background(), args, tctx)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != GrepScopeRequiredCode {
		t.Fatalf("second call err = %v want %s", err, GrepScopeRequiredCode)
	}
}

func TestGrepUnknownCountFailSafeDenseness(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.go"), "package a\nconst needle = 1\n")
	tool := &GrepTool{
		Boundary:  nativefixture.Boundary(t),
		Scope:     ptrScope(toolscope.Default()),
		FileCount: func(string) (int, bool) { return 0, false },
	}
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "s-unknown"},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Path: dir, IsPrimary: true}},
			ActiveRootID: "r1"},
	}
	out, err := tool.Run(context.Background(), map[string]any{"pattern": "needle", "path": "."}, tctx)
	testutil.FailErr(t, "unknown denseness", err)
	if !strings.Contains(out, `"view":"digest"`) && !strings.Contains(out, `"view": "digest"`) {
		t.Fatalf("expected denseness digest, got %s", out)
	}
}

func TestGrepScopedPathBypassesGuard(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "browser", "components")
	testutil.FailErr(t, "mkdir", os.MkdirAll(sub, 0o755))
	mustWrite(t, filepath.Join(sub, "x.go"), "package x\nconst needle = 1\n")
	scope := toolscope.Default()
	scope.RootStructuralFileCount = 10
	tool := &GrepTool{Boundary: nativefixture.Boundary(t), Scope: &scope}
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "s-scoped"},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Path: dir, IsPrimary: true}},
			ActiveRootID:       "r1",
			RepoFileCount:      200_000,
			RepoFileCountKnown: true},
	}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern": "needle",
		"path":    "browser/components",
	}, tctx)
	testutil.FailErr(t, "scoped grep", err)
	if !strings.Contains(out, "needle") {
		t.Fatalf("scoped grep should find match: %s", out)
	}
}

func TestGrepNarrowPathGlobBypassesGuard(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.go"), "package a\nconst needle = 1\n")
	mustWrite(t, filepath.Join(dir, "b.md"), "needle\n")
	scope := toolscope.Default()
	scope.RootStructuralFileCount = 10
	tool := &GrepTool{Boundary: nativefixture.Boundary(t), Scope: &scope}
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "s-glob"},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Path: dir, IsPrimary: true}},
			ActiveRootID:       "r1",
			RepoFileCount:      200_000,
			RepoFileCountKnown: true},
	}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern":   "needle",
		"path":      ".",
		"path_glob": "*.go",
	}, tctx)
	testutil.FailErr(t, "glob grep", err)
	if !strings.Contains(out, "a.go") {
		t.Fatalf("narrow glob should search: %s", out)
	}
}

func ptrScope(c toolscope.Config) *toolscope.Config { return &c }

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	testutil.FailErr(t, "write "+path, os.WriteFile(path, []byte(body), 0o644))
}
