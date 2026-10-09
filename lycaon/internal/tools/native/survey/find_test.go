package survey

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

func parseFindResponse(t *testing.T, out string) findResponse {
	t.Helper()
	var resp findResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode find response: %v raw=%s", err, out)
	}
	return resp
}

func TestFindToolNameGlobGoFiles(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "internal", "pkg"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	for _, p := range []struct{ path, content string }{
		{"main.go", "package main"},
		{"internal/pkg/foo.go", "package pkg"},
		{"internal/pkg/readme.txt", "hi"},
		{"skip.py", "print(1)"},
	} {
		if err := os.WriteFile(filepath.Join(tmpDir, p.path), []byte(p.content), 0o644); err != nil {
			testutil.FailErr(t, "write", err)
		}
	}

	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"name_glob": "**/*.go",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "find", err)
	nativefixture.AssertReceipt(t, out)
	resp := parseFindResponse(t, out)
	if len(resp.Results) != 2 {
		t.Fatalf("results = %+v", resp.Results)
	}
	for _, r := range resp.Results {
		if !strings.HasSuffix(r.Path, ".go") || r.Type != "file" {
			t.Fatalf("entry = %+v", r)
		}
	}
}

func TestFindToolNameGlobSimpleExtension(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "lycaon", "internal"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	for _, p := range []string{"main.go", "lycaon/internal/tools.go", "readme.txt"} {
		if err := os.WriteFile(filepath.Join(tmpDir, p), []byte("x"), 0o644); err != nil {
			testutil.FailErr(t, "write", err)
		}
	}
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"name_glob": "*.go",
		"type":      "file",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "find simple name_glob", err)
	resp := parseFindResponse(t, out)
	if len(resp.Results) != 2 {
		t.Fatalf("results = %+v want 2 go files", resp.Results)
	}
}

func TestFindToolEmptyResult(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "only.txt"), []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write", err)
	}
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"name_glob": "**/*.go",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "find", err)
	resp := parseFindResponse(t, out)
	if len(resp.Results) != 0 || resp.Truncated {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestFindToolDepthCap(t *testing.T) {
	tmpDir := t.TempDir()
	leaf := tmpDir
	for i := 0; i < 10; i++ {
		leaf = filepath.Join(leaf, fmt.Sprintf("d%d", i))
		if err := os.Mkdir(leaf, 0o755); err != nil {
			testutil.FailErr(t, "mkdir", err)
		}
	}
	if err := os.WriteFile(filepath.Join(leaf, "deep.go"), []byte("package deep"), 0o644); err != nil {
		testutil.FailErr(t, "write", err)
	}

	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"max_depth": 3,
		"type":      "file",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "find", err)
	resp := parseFindResponse(t, out)
	if len(resp.Results) != 0 {
		t.Fatalf("expected no files within depth 3, got %+v", resp.Results)
	}
}

func TestFindToolMaxResultsClampBanner(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "one.txt"), []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write", err)
	}
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"max_results": float64(99999),
		"type":        "file",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "find clamp", err)
	resp := parseFindResponse(t, out)
	if resp.MaxResults != safecmd.FindMaxResults {
		t.Fatalf("max_results = %d want %d", resp.MaxResults, safecmd.FindMaxResults)
	}
	if !strings.Contains(resp.TruncationBanner, fmt.Sprintf("max_results capped at %d", safecmd.FindMaxResults)) {
		t.Fatalf("banner = %q", resp.TruncationBanner)
	}
}

func TestFindToolMaxResultsTruncated(t *testing.T) {
	tmpDir := t.TempDir()
	for i := 0; i < 550; i++ {
		name := fmt.Sprintf("file_%04d.txt", i)
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte("x"), 0o644); err != nil {
			testutil.FailErr(t, "write", err)
		}
	}
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"max_results": 500,
		"type":        "file",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "find", err)
	resp := parseFindResponse(t, out)
	if len(resp.Results) != 500 {
		t.Fatalf("len(results) = %d want 500 literal page", len(resp.Results))
	}
	if resp.View != "" {
		t.Fatalf("view = %q want empty on literal", resp.View)
	}
	if resp.TotalResults != 550 {
		t.Fatalf("total_results = %d want 550", resp.TotalResults)
	}
	if !resp.Truncated || resp.NextOffset == nil || *resp.NextOffset != 500 {
		t.Fatalf("expected truncated literal page, got %+v", resp)
	}
}

func TestFindToolMultiRootCountsMatchesAfterPageFills(t *testing.T) {
	primary := t.TempDir()
	secondary := t.TempDir()
	for i := 0; i < 501; i++ {
		name := fmt.Sprintf("file_%04d.txt", i)
		testutil.FailErr(t, "write primary", os.WriteFile(filepath.Join(primary, name), []byte("x"), 0o644))
	}
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("later_%04d.txt", i)
		testutil.FailErr(t, "write secondary", os.WriteFile(filepath.Join(secondary, name), []byte("x"), 0o644))
	}
	tctx := nativefixture.Context(primary)
	tctx.Roots = []projectroot.RootRef{
		{ID: "p", Label: "primary", Path: primary, IsPrimary: true},
		{ID: "s", Label: "secondary", Path: secondary},
	}
	tctx.ActiveRootID = "p"
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"max_results": 500, "type": "file"}, tctx)
	testutil.FailErr(t, "find multi-root", err)
	resp := parseFindResponse(t, out)
	if resp.TotalResults != 504 {
		t.Fatalf("total_results = %d want 504", resp.TotalResults)
	}
	if len(resp.Results) != 500 || !resp.Truncated {
		t.Fatalf("page = %d truncated=%v", len(resp.Results), resp.Truncated)
	}
}

func TestFindToolOffsetPagination(t *testing.T) {
	tmpDir := t.TempDir()
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("file_%d.txt", i)
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte("x"), 0o644); err != nil {
			testutil.FailErr(t, "write", err)
		}
	}
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"type":        "file",
		"max_results": 2,
		"offset":      2,
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "find offset", err)
	resp := parseFindResponse(t, out)
	if len(resp.Results) != 2 || resp.Results[0].Path != "file_2.txt" {
		t.Fatalf("results = %+v", resp.Results)
	}
	if resp.Offset != 2 || !resp.Truncated || resp.NextOffset == nil || *resp.NextOffset != 4 {
		t.Fatalf("pagination = offset=%d truncated=%v next=%v", resp.Offset, resp.Truncated, resp.NextOffset)
	}
}

func TestFindToolPathNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{"path": "missing/dir"}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "FIND_PATH_NOT_FOUND" {
		t.Fatalf("err = %v want FIND_PATH_NOT_FOUND", err)
	}
}

func TestFindToolPathEscape(t *testing.T) {
	tmpDir := t.TempDir()
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{"path": "../outside"}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "SURVEY_PATH_ESCAPE" {
		t.Fatalf("err = %v want SURVEY_PATH_ESCAPE", err)
	}
}

func TestFindToolTypeDir(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmpDir, "pkg"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "pkg", "a.go"), []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write", err)
	}
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"type": "dir"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "find", err)
	resp := parseFindResponse(t, out)
	if len(resp.Results) != 1 || resp.Results[0].Path != "pkg" || resp.Results[0].Type != "dir" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestFindToolSkipsGitDir(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".git", "objects"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".git", "objects", "secret"), []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "visible.go"), []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write", err)
	}
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"type": "file"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "find", err)
	resp := parseFindResponse(t, out)
	if len(resp.Results) != 1 || resp.Results[0].Path != "visible.go" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestFindToolDeeperPathsOmitted(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "a"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir a", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "a", "shallow.go"), []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write shallow", err)
	}
	deep := filepath.Join(tmpDir, "a", "b", "c", "d", "e", "f", "g", "h", "i")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		testutil.FailErr(t, "mkdir deep", err)
	}
	if err := os.WriteFile(filepath.Join(deep, "deep.go"), []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write deep", err)
	}
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"path":      ".",
		"type":      "file",
		"name_glob": "**/*.go",
		"max_depth": float64(3),
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "find depth", err)
	resp := parseFindResponse(t, out)
	if !resp.DeeperPathsOmitted {
		t.Fatalf("expected deeper_paths_omitted=true, resp=%+v", resp)
	}
	if resp.DepthNotice == "" {
		t.Fatalf("expected depth_notice, resp=%+v", resp)
	}
	if len(resp.Results) != 1 || resp.Results[0].Path != "a/shallow.go" {
		t.Fatalf("results = %+v", resp.Results)
	}
	if strings.Contains(out, "deep.go") {
		t.Fatalf("deep file should be clipped: %q", out)
	}
}

func writeDeepFile(t *testing.T, root string, depth int, name string) string {
	t.Helper()
	parts := make([]string, 0, depth)
	for i := range depth - 1 {
		parts = append(parts, fmt.Sprintf("d%d", i))
	}
	dir := filepath.Join(append([]string{root}, parts...)...)
	testutil.FailErr(t, "mkdir deep", os.MkdirAll(dir, 0o755))
	testutil.FailErr(t, "write deep", os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644))
	return filepath.ToSlash(filepath.Join(append(parts, name)...))
}

// A name search reaches every depth: the glob is the filter, and a depth
// default would hide matches by where they live.
func TestFindToolNameGlobSearchesEveryDepth(t *testing.T) {
	tmpDir := t.TempDir()
	want := writeDeepFile(t, tmpDir, 14, "SECURITY.md")
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"name_glob": "**/SECURITY.md"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "find deep name", err)
	resp := parseFindResponse(t, out)
	if len(resp.Results) != 1 || resp.Results[0].Path != want {
		t.Fatalf("results = %+v, want %s", resp.Results, want)
	}
	if resp.MaxDepth != 0 || resp.DeeperPathsOmitted || resp.DepthNotice != "" {
		t.Fatalf("unbounded name search reported a depth limit: %+v", resp)
	}
}

// A listing keeps its shaping depth, and a file just past it raises the notice.
func TestFindToolListingKeepsDefaultDepth(t *testing.T) {
	tmpDir := t.TempDir()
	testutil.FailErr(t, "write shallow", os.WriteFile(filepath.Join(tmpDir, "top.go"), []byte("x"), 0o644))
	deep := writeDeepFile(t, tmpDir, safecmd.FindListingDepth+1, "deep.go")
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"type": "file", "max_results": 100}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "find listing", err)
	resp := parseFindResponse(t, out)
	if resp.MaxDepth != safecmd.FindListingDepth || !resp.DeeperPathsOmitted {
		t.Fatalf("listing depth = %d omitted=%v, want %d and true", resp.MaxDepth, resp.DeeperPathsOmitted, safecmd.FindListingDepth)
	}
	if len(resp.Results) != 1 || resp.Results[0].Path != "top.go" || strings.Contains(out, deep) {
		t.Fatalf("listing results = %+v", resp.Results)
	}
}
