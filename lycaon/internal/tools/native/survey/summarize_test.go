package survey

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func testSummarizeTool(t *testing.T, dir string) *SummarizeTool {
	t.Helper()
	caps := summarize.DefaultCaps()
	caps.Gather.IndexWaitMs = 30_000
	return &SummarizeTool{
		Boundary: nativefixture.Boundary(t),
		Caps:     caps,
	}
}

func decodeSummarizeResponse(t *testing.T, raw string) summarizeResponse {
	t.Helper()
	var resp summarizeResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("decode summarize response: %v", err)
	}
	return resp
}

func assertSummarizeReject(t *testing.T, err error, wantCode string) {
	t.Helper()
	var reject *toolrejection.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != wantCode {
		t.Fatalf("err = %v, want reject code %q", err, wantCode)
	}
}

func TestSummarizeToolRejectCodes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pkg/a.go", "package main\n")
	tool := testSummarizeTool(t, dir)
	ctx := context.Background()
	tctx := nativefixture.Context(dir)

	cases := []struct {
		name string
		args map[string]any
		code string
	}{
		{"no input at all", map[string]any{}, "SUMMARIZE_NO_INPUT"},
		{"task only, no material", map[string]any{"task": "explain"}, "SUMMARIZE_NO_INPUT"},
		{"pattern needs path", map[string]any{"task": "explain", "pattern": "func Main"}, "SUMMARIZE_PATTERN_NEEDS_PATH"},
		{"content too large", map[string]any{
			"task":    "explain",
			"content": strings.Repeat("x", summarize.DefaultCaps().Gather.InlineMaxBytes+1),
		}, "SUMMARIZE_CONTENT_TOO_LARGE"},
		{"empty paths", map[string]any{"task": "explain", "paths": []any{}}, "SUMMARIZE_NO_INPUT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tool.Run(ctx, tc.args, tctx)
			assertSummarizeReject(t, err, tc.code)
		})
	}

	t.Run("content below floor returns verbatim", func(t *testing.T) {
		const tiny = "tiny paste"
		raw, err := tool.Run(ctx, map[string]any{"task": "explain", "content": tiny}, tctx)
		testutil.FailErr(t, "run", err)
		resp := decodeSummarizeResponse(t, raw)
		if len(resp.Pack.Substance) != 1 || resp.Pack.Substance[0].Body != tiny {
			t.Fatalf("pack substance = %+v, want verbatim [%q]", resp.Pack.Substance, tiny)
		}
		if !resp.Coverage.Complete {
			t.Fatalf("coverage = %+v", resp.Coverage)
		}
		if resp.Gather.Mode != string(summarize.ModeInline) || resp.Gather.Bytes != len(tiny) {
			t.Fatalf("gather = %+v", resp.Gather)
		}
	})

	t.Run("content below floor parseable go runs structure", func(t *testing.T) {
		const snippet = "package main\n\nfunc Hello() string { return \"hi\" }\n"
		if len(snippet) >= summarize.DefaultCaps().Gather.InlineMinBytes {
			t.Fatalf("snippet too large for floor test: %d", len(snippet))
		}
		raw, err := tool.Run(ctx, map[string]any{"task": "what does this export", "content": snippet}, tctx)
		testutil.FailErr(t, "run", err)
		resp := decodeSummarizeResponse(t, raw)
		// Structure ran (not verbatim passthrough): skeleton carries the symbol.
		if len(resp.Pack.Skeleton) == 0 {
			t.Fatalf("expected structure pack skeleton for parseable snippet, got %+v", resp.Pack)
		}
	})

	t.Run("single path missing rejects structured", func(t *testing.T) {
		_, err := tool.Run(ctx, map[string]any{"path": "no/such/file.go"}, tctx)
		assertSummarizeReject(t, err, "SUMMARIZE_PATHS_MISSING")
	})

	t.Run("path without task runs", func(t *testing.T) {
		if _, err := tool.Run(ctx, map[string]any{"path": "pkg/a.go"}, tctx); err != nil {
			var reject *toolrejection.ToolReject
			if errors.As(err, &reject) {
				t.Fatalf("summarize(path) without task rejected: %v", reject.Code)
			}
			t.Fatalf("summarize(path) without task errored: %v", err)
		}
	})

	t.Run("no material", func(t *testing.T) {
		emptyDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(emptyDir, "empty"), 0o750); err != nil {
			testutil.FailErr(t, "mkdir empty", err)
		}
		emptyTool := testSummarizeTool(t, emptyDir)
		_, err := emptyTool.Run(ctx, map[string]any{"task": "explain", "path": "empty"}, nativefixture.Context(emptyDir))
		assertSummarizeReject(t, err, "SUMMARIZE_NO_MATERIAL")
	})
}

func TestSummarizeModeResolution(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pkg/a.go", "package main\n")
	tool := testSummarizeTool(t, dir)
	ctx := context.Background()

	inline, err := tool.Run(ctx, map[string]any{
		"task":    "explain",
		"content": strings.Repeat("line\n", 50),
	}, nativefixture.Context(dir))
	testutil.FailErr(t, "inline run", err)
	if got := decodeSummarizeResponse(t, inline).Gather.Mode; got != string(summarize.ModeInline) {
		t.Fatalf("inline mode = %q", got)
	}

	repo, err := tool.Run(ctx, map[string]any{"task": "explain", "path": "pkg/a.go"}, nativefixture.Context(dir))
	testutil.FailErr(t, "repo run", err)
	if got := decodeSummarizeResponse(t, repo).Gather.Mode; got != string(summarize.ModeRepo) {
		t.Fatalf("repo mode = %q", got)
	}

	hybrid, err := tool.Run(ctx, map[string]any{
		"task":    "explain",
		"content": strings.Repeat("line\n", 50),
		"path":    "pkg/a.go",
	}, nativefixture.Context(dir))
	testutil.FailErr(t, "hybrid run", err)
	if got := decodeSummarizeResponse(t, hybrid).Gather.Mode; got != string(summarize.ModeHybrid) {
		t.Fatalf("hybrid mode = %q", got)
	}
}

func TestSummarizePinsAuthorizationAndKeepsReferenceLeadsAtScale(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/app\n\ngo 1.22\n")
	writeFile(t, dir, "pkg/target.go", "package pkg\n\nfunc TargetFn() {}\n")
	writeFile(t, dir, "cmd/main.go", "package main\n\nimport \"example.com/app/pkg\"\n\nfunc main() { pkg.TargetFn() }\n")
	for i := range 256 {
		writeFile(t, dir, fmt.Sprintf("noise/file-%03d.go", i), fmt.Sprintf("package noise\nfunc Noise%03d() {}\n", i))
	}

	profile := sandbox.ToolProfile{ID: toolprofiles.DefaultToolProfileID, Tools: map[string]bool{"summarize": true}}
	boundary := sandbox.NewBoundary(sandbox.Config{
		ProjectRootRequired: true,
		RejectSymlinkEscape: true,
	}, []sandbox.ToolProfile{profile})
	var profileLookups atomic.Int64
	boundary.SetProfileSource(func(_ context.Context, sessionID string) []sandbox.ToolProfile {
		if sessionID != "large-session" {
			t.Fatalf("profile source session = %q", sessionID)
		}
		profileLookups.Add(1)
		return []sandbox.ToolProfile{profile}
	})
	tctx := testCtxSession(dir, "large-session")
	ctx := tools.SandboxScopeContext(context.Background(), tctx)
	tool := &SummarizeTool{
		Boundary: boundary,
		Caps:     summarize.DefaultCaps(),
		Catalog:  sourcecatalog.New(),
	}
	tool.Caps.Gather.CallSiteMax = 12
	tool.Caps.Gather.NeighborMax = 6

	raw, err := tool.Run(ctx, map[string]any{"path": "pkg/target.go", "task": "TargetFn callers"}, tctx)
	testutil.FailErr(t, "summarize large repository", err)
	resp := decodeSummarizeResponse(t, raw)
	if !resp.Coverage.Complete {
		t.Fatalf("coverage = %+v", resp.Coverage)
	}
	if len(resp.Pack.CallSites) == 0 || len(resp.Pack.Neighbors) == 0 {
		t.Fatalf("reference leads lost: call_sites=%+v neighbors=%+v", resp.Pack.CallSites, resp.Pack.Neighbors)
	}
	if got := profileLookups.Load(); got != 1 {
		t.Fatalf("effective profile lookups = %d, want one invocation snapshot", got)
	}
}

func TestSummarizeCommaSeparatedPathPromoted(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pkg/a.go", "package pkg\nfunc A() {}\n")
	writeFile(t, dir, "pkg/b.go", "package pkg\nfunc B() {}\n")
	tool := testSummarizeTool(t, dir)

	raw, err := tool.Run(context.Background(), map[string]any{
		"task": "compare",
		"path": "pkg/a.go,pkg/b.go",
	}, nativefixture.Context(dir))
	testutil.FailErr(t, "comma path run", err)
	resp := decodeSummarizeResponse(t, raw)
	if resp.Gather.Path != "" {
		t.Fatalf("path should be cleared after promotion, got %q", resp.Gather.Path)
	}
	if len(resp.Gather.Paths) != 2 {
		t.Fatalf("paths = %v, want 2 promoted entries", resp.Gather.Paths)
	}
}

func TestSummarizeMultiPathCursorKeepsEveryTarget(t *testing.T) {
	dir := t.TempDir()
	paths := make([]any, 16)
	for i := range paths {
		path := fmt.Sprintf("pkg/file-%02d.go", i)
		writeFile(t, dir, path, fmt.Sprintf("package pkg\nfunc File%02d() {}\n", i))
		paths[i] = path
	}
	caps := summarize.DefaultCaps()
	caps.Pack.SubtreeFanoutMax = 4
	tool := &SummarizeTool{Boundary: nativefixture.Boundary(t), Caps: caps}

	raw, err := tool.Run(context.Background(), map[string]any{
		"task": "survey targets", "paths": paths,
	}, nativefixture.Context(dir))
	testutil.FailErr(t, "initial summarize", err)
	resp := decodeSummarizeResponse(t, raw)
	if len(resp.Gather.Paths) != len(paths) || resp.Coverage.FilesTotal != len(paths) {
		t.Fatalf("gather=%d files=%d, want %d", len(resp.Gather.Paths), resp.Coverage.FilesTotal, len(paths))
	}
	if !resp.Coverage.Complete || resp.Coverage.NextCursor == "" {
		t.Fatalf("coverage = %+v", resp.Coverage)
	}

	var continuation *summarize.NextAction
	for i := range resp.NextActions {
		if resp.NextActions[i].Cursor != "" {
			continuation = &resp.NextActions[i]
			break
		}
	}
	if continuation == nil || len(continuation.Paths) != len(paths) {
		t.Fatalf("next actions = %+v", resp.NextActions)
	}
	raw, err = tool.Run(context.Background(), map[string]any{
		"task": "survey targets", "paths": paths, "cursor": continuation.Cursor,
	}, nativefixture.Context(dir))
	testutil.FailErr(t, "continued summarize", err)
	continued := decodeSummarizeResponse(t, raw)
	if continued.Coverage.Cursor != continuation.Cursor || continued.Coverage.FilesTotal != len(paths) {
		t.Fatalf("continued coverage = %+v", continued.Coverage)
	}
}

func TestSplitCommaSeparatedPaths(t *testing.T) {
	got := splitCommaSeparatedPaths("pkg/a.go, pkg/b.go")
	if len(got) != 2 || got[0] != "pkg/a.go" || got[1] != "pkg/b.go" {
		t.Fatalf("got %v", got)
	}
	if splitCommaSeparatedPaths("pkg/a.go") != nil {
		t.Fatal("single path must not split")
	}
	if splitCommaSeparatedPaths("hello, world") != nil {
		t.Fatal("prose must not split")
	}
}

func TestSummarizeWireAssembly(t *testing.T) {
	res := summarize.Result{
		Task: "task",
		Pack: summarize.ContextPack{
			Identity:  []summarize.PackIdentity{{Path: "p.go", Kind: "file", LineCount: 10, ParseHealth: "ok"}},
			Skeleton:  []summarize.PackSymbol{{Path: "p.go", Kind: "func", Name: "A", Line: 2}},
			Substance: []summarize.PackWindow{{Path: "p.go", StartLine: 1, EndLine: 3, Body: "func A() {}"}},
		},
		Anchors:     []summarize.Anchor{{Path: "p.go", Line: 2, Excerpt: "ex", Handle: "summarize#1"}},
		NextActions: []summarize.NextAction{{Tool: "read", Path: "p.go", Why: "why"}},
		Coverage: summarize.Coverage{
			Complete: true, FilesTotal: 3, FilesRepresented: 3, AnchorsReturned: 1,
		},
		Sources: []string{"p.go"},
		Gather: summarize.GatherReport{
			Mode: summarize.ModeRepo, Path: "pkg", Pattern: "func",
			Candidates: 3, Bytes: 100,
		},
		Orchestration: summarize.OrchestrationReport{
			AssembleMs: 5,
		},
	}
	resp := buildSummarizeResponse(res)
	if resp.Task != "task" || resp.Coverage.AnchorsReturned != 1 || resp.Coverage.FilesTotal != 3 {
		t.Fatalf("basic fields = %+v", resp)
	}
	if len(resp.Pack.Identity) != 1 || len(resp.Pack.Skeleton) != 1 || len(resp.Pack.Substance) != 1 {
		t.Fatalf("pack tiers = %+v", resp.Pack)
	}
	if !resp.Coverage.Complete {
		t.Fatalf("coverage = %+v", resp.Coverage)
	}
	if len(resp.Anchors) != 1 || resp.Anchors[0].Handle != "" {
		t.Fatalf("wire anchors omit handle: %+v", resp.Anchors)
	}
	if resp.Gather.Mode != string(summarize.ModeRepo) {
		t.Fatalf("gather = %+v", resp.Gather)
	}
}

func TestSummarizeUsesHostDefaultAnchors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pkg/a.go", "package main\n")
	caps := summarize.DefaultCaps()
	tool := &SummarizeTool{
		Boundary: nativefixture.Boundary(t),
		Caps:     caps,
	}
	raw, err := tool.Run(context.Background(), map[string]any{
		"task": "explain", "path": "pkg/a.go",
	}, nativefixture.Context(dir))
	testutil.FailErr(t, "run", err)
	_ = decodeSummarizeResponse(t, raw)
	if got := caps.ClampAnchors(0); got != caps.Anchors.Default {
		t.Fatalf("ClampAnchors(0) = %d want default %d", got, caps.Anchors.Default)
	}
}

func TestSummarizeToolRunsWithoutOptionalDeps(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pkg/a.go", "package main\nfunc A() {}\n")
	tool := testSummarizeTool(t, dir)
	raw, err := tool.Run(context.Background(), map[string]any{
		"task": "explain", "path": "pkg/a.go",
	}, nativefixture.Context(dir))
	testutil.FailErr(t, "run", err)
	resp := decodeSummarizeResponse(t, raw)
	if len(resp.Pack.Identity) == 0 && len(resp.Pack.Skeleton) == 0 && len(resp.Pack.Substance) == 0 {
		t.Fatal("expected non-empty pack")
	}
}
