package survey

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestSummarizeMultiplePathsPinOneGenerationPerRoot(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a/source.go", "package a")
	writeFile(t, dir, "b/source.go", "package b")
	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	catalog := sourcecatalog.New()
	g.access.catalog, g.access.trees, g.access.literalsIndex = catalog, catalog.Trees, catalog.Literals
	first := g.trees.buildSubtreeForTarget(t.Context(), "a")
	if first == nil || len(g.trees.treeReaders) != 1 {
		t.Fatal("first scope did not pin an indexed generation")
	}
	writeFile(t, dir, "b/new.go", "package b")
	catalog.InvalidateRoot(dir, "b/new.go")
	resolved, err := g.access.reads.Resolve(t.Context(), ".")
	testutil.FailErr(t, "resolve indexed root", err)
	scope, err := g.access.boundary.CompileReadScope(t.Context(), resolved.Root.Path, g.access.profileID)
	testutil.FailErr(t, "compile read scope", err)
	fresh, _, err := g.access.trees.OpenSummary(t.Context(), g.access.projectID, sourcecatalog.Root{ID: resolved.Root.ID, Path: resolved.Root.Path}, sourcecatalog.TreeScope{Key: scope.Key, Filter: scope.Filter, PruneNestedVCS: g.caps.Gather.PruneNestedVCS}, 30*time.Second)
	testutil.FailErr(t, "publish intervening edit", err)
	if fresh == nil {
		t.Fatal("updated index unavailable")
	}
	defer func() { _ = fresh.Close() }()
	second := g.trees.buildSubtreeForTarget(t.Context(), "b")
	if second == nil || second.Material.SourceFiles != 1 || first.Revision != second.Revision || len(g.trees.treeReaders) != 1 {
		t.Fatalf("inconsistent scope generations: first=%+v second=%+v readers=%d", first, second, len(g.trees.treeReaders))
	}
}

func TestSummarizePartialSourceDoesNotClaimWholeFileDiagnostics(t *testing.T) {
	dir := t.TempDir()
	body := "package source\nfunc Opening() {\n" + strings.Repeat("// more source\n", 1000) + "}\n"
	writeFile(t, dir, "large.go", body)
	caps := summarize.DefaultCaps()
	caps.Gather.FileReadBytes = 128
	caps.Gather.MaxBytes = 256
	g := testSummarizeGatherer(t, dir, caps)
	sc, _, ok := g.sources.structureFromAbs(t.Context(), filepath.Join(dir, "large.go"), "large.go")
	if !ok || sc.Head == "" || sc.LineCount != 0 || sc.Parses != nil || len(sc.Errors) > 0 || sc.ErrorKind != "" {
		t.Fatalf("partial observation=%+v ok=%v", sc, ok)
	}
	if g.sources.sourceReadBytes != 128 || !g.sources.sourceLimited {
		t.Fatalf("source bytes=%d limited=%v", g.sources.sourceReadBytes, g.sources.sourceLimited)
	}
	_, _, _ = g.sources.structureFromAbs(t.Context(), filepath.Join(dir, "large.go"), "large.go")
	if g.sources.sourceReadBytes != 128 || g.sources.sourceFiles != 1 {
		t.Fatalf("memoized source reread: bytes=%d files=%d", g.sources.sourceReadBytes, g.sources.sourceFiles)
	}
}

func TestSummarizeIndexedPagesCoverWideDirectory(t *testing.T) {
	dir := t.TempDir()
	for i := range 93 {
		writeFile(t, dir, fmt.Sprintf("item-%03d.go", i), fmt.Sprintf("package sample\nfunc Item%d() string { return \"useful detail\" }\n", i))
	}
	tool := testSummarizeTool(t, dir)
	var observed summarize.Result
	tool.Observe = func(result summarize.Result) { observed = result }
	_ = observed
	tool.Caps.Pack.SubtreeFanoutMax = 8
	tool.Caps.Gather.MetadataNodes = 32
	tool.Caps.Gather.MaxFilesRead = 3
	tool.Caps.Gather.MaxBytes = 1024
	tool.Catalog = sourcecatalog.New()
	seen := map[string]bool{}
	args := map[string]any{"path": ".", "task": "item"}
	for page := 0; page < 20; page++ {
		raw, err := tool.Run(t.Context(), args, nativefixture.Context(dir))
		testutil.FailErr(t, "summarize indexed page", err)
		res := decodeSummarizeResponse(t, raw)
		if !res.Coverage.Complete || res.Coverage.FilesTotal != 93 || res.Coverage.ChildrenTotal != 93 {
			t.Fatalf("coverage=%+v", res.Coverage)
		}
		stats := observed.Orchestration.Curator
		if stats.MetadataRowsRead > 100 || stats.SourceFilesRead > 3 || stats.SourceBytesRead > 1024 {
			t.Fatalf("work=%+v", stats)
		}
		for _, row := range res.Pack.Skeleton {
			if strings.HasPrefix(row.Path, "item-") {
				seen[row.Path] = true
			}
		}
		if res.Coverage.NextCursor == "" {
			break
		}
		args = map[string]any{"path": ".", "task": "item", "cursor": res.Coverage.NextCursor}
	}
	if len(seen) != 93 {
		t.Fatalf("represented %d unique files, want 93", len(seen))
	}
}

func TestSummarizeTaskFindsBranchOutsideFirstCatalogPage(t *testing.T) {
	dir := t.TempDir()
	for i := range 100 {
		writeFile(t, dir, fmt.Sprintf("branch-%03d/a.go", i), "package sample\nfunc Ordinary() {}\n")
	}
	writeFile(t, dir, "zzz/authorization/permissions.go", "package authorization\nfunc CheckPermission() string { return \"access granted\" }\n")
	tool := testSummarizeTool(t, dir)
	var observed summarize.Result
	tool.Observe = func(result summarize.Result) { observed = result }
	_ = observed
	tool.Caps.Pack.SubtreeFanoutMax = 8
	tool.Caps.Gather.MetadataNodes = 64
	raw, err := tool.Run(t.Context(), map[string]any{"path": ".", "task": "authorization permissions"}, nativefixture.Context(dir))
	testutil.FailErr(t, "summarize relevant branch", err)
	res := decodeSummarizeResponse(t, raw)
	if res.Coverage.FilesTotal != 101 {
		t.Fatalf("coverage=%+v", res.Coverage)
	}
	found := false
	for _, anchor := range res.Anchors {
		if anchor.Path == "zzz/authorization/permissions.go" && strings.Contains(anchor.Excerpt, "CheckPermission") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing grounded task detail: %+v", res)
	}
}

func TestSummarizeCursorRejectsChangedGenerationAndTask(t *testing.T) {
	dir := t.TempDir()
	for i := range 20 {
		writeFile(t, dir, fmt.Sprintf("file-%02d.go", i), "package sample\nfunc Original() {}\n")
	}
	tool := testSummarizeTool(t, dir)
	var observed summarize.Result
	tool.Observe = func(result summarize.Result) { observed = result }
	_ = observed
	tool.Catalog = sourcecatalog.New()
	tool.Caps.Pack.SubtreeFanoutMax = 4
	raw, err := tool.Run(t.Context(), map[string]any{"path": ".", "task": "original"}, nativefixture.Context(dir))
	testutil.FailErr(t, "first page", err)
	cursor := decodeSummarizeResponse(t, raw).Coverage.NextCursor
	if cursor == "" {
		t.Fatal("missing continuation")
	}
	_, err = tool.Run(t.Context(), map[string]any{"path": ".", "task": "changed", "cursor": cursor}, nativefixture.Context(dir))
	assertSummarizeReject(t, err, "SUMMARIZE_CURSOR_STALE")
	writeFile(t, dir, "file-00.go", "package sample\nfunc Changed() {}\n")
	tool.Catalog.InvalidateRoot(dir, "file-00.go")
	_, err = tool.Run(t.Context(), map[string]any{"path": ".", "task": "original", "cursor": cursor}, nativefixture.Context(dir))
	assertSummarizeReject(t, err, "SUMMARIZE_CURSOR_STALE")
}

func TestSummarizeOverlappingScopesCountEachFileOnce(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "src/a.go", "package source\nfunc A() {}\n")
	writeFile(t, dir, "src/b.go", "package source\nfunc B() {}\n")
	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	res, err := g.Gather(t.Context(), summarize.Request{Paths: []string{"src/a.go", "src", "src/b.go"}})
	testutil.FailErr(t, "overlapping scope", err)
	if res.Subtree.Material.SourceFiles != 2 || len(res.Subtree.Children) != 1 {
		t.Fatalf("scope=%+v", res.Subtree)
	}
	if res.Subtree.Children[0].Path != "src" {
		t.Fatalf("subdirectory path=%q", res.Subtree.Children[0].Path)
	}
}

func TestSummarizeScopeUnionHandlesSimilarSiblingNames(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a/b/source.go", "package source")
	writeFile(t, dir, "a-sibling/source.go", "package source")
	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	res, err := g.Gather(t.Context(), summarize.Request{Paths: []string{"a", "a-sibling", "a/b"}})
	testutil.FailErr(t, "scope union", err)
	if res.Subtree.Material.SourceFiles != 2 || len(res.Subtree.Children) != 2 {
		t.Fatalf("overlapping scope material=%+v children=%d", res.Subtree.Material, len(res.Subtree.Children))
	}
}

func TestSummarizeWarmOrientationPreservesReadmeAndUnknownCounts(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "# Project purpose\nThis repository coordinates source indexing.\n")
	writeFile(t, dir, "subtree/file.go", "package source\n")
	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	resolved, err := g.access.reads.Resolve(t.Context(), ".")
	testutil.FailErr(t, "resolve scope", err)
	root := g.trees.warmingSubtree(context.Background(), resolved)
	if !root.UnknownMaterial || root.Material.SourceFiles != 0 || !subtreeContains(root, "README.md") {
		t.Fatalf("warm orientation=%+v", root)
	}
	if strings.Contains(strings.Join(sourceDirMapCandidate(root).RollupRows, "\n"), "subtree files=0") {
		t.Fatal("unknown subtree presented as empty")
	}
}

func TestSummarizeReadBudgetPreservesUTF8Prefix(t *testing.T) {
	for _, chunk := range []int{16, 1024} {
		t.Run(fmt.Sprint(chunk), func(t *testing.T) {
			dir := t.TempDir()
			body := "# Useful orientation\n" + strings.Repeat("界", 100)
			writeFile(t, dir, "README.md", body)
			caps := summarize.DefaultCaps()
			caps.Gather.FileReadBytes = len("# Useful orientation\n") + 4
			caps.Gather.FileChunkBytes = chunk
			g := testSummarizeGatherer(t, dir, caps)
			sc, _, ok := g.sources.structureFromAbs(t.Context(), filepath.Join(dir, "README.md"), "README.md")
			if !ok || !strings.Contains(sc.Head, "Useful") || sc.LineCount != 0 {
				t.Fatalf("truncated UTF-8 observation=%+v ok=%v", sc, ok)
			}
		})
	}
}

func TestSummarizePatternDetailBudgetProducesContinuation(t *testing.T) {
	dir := t.TempDir()
	for i := range 3 {
		writeFile(t, dir, fmt.Sprintf("file-%d.txt", i), "Needle\n"+strings.Repeat("material\n", 100))
	}
	tool := testSummarizeTool(t, dir)
	var observed summarize.Result
	tool.Observe = func(result summarize.Result) { observed = result }
	_ = observed
	tool.Caps.Gather.MaxBytes = 128
	tool.Caps.Gather.FileReadBytes = 128
	seen := map[string]bool{}
	args := map[string]any{"path": ".", "pattern": "Needle"}
	for page := range 3 {
		raw, err := tool.Run(t.Context(), args, nativefixture.Context(dir))
		testutil.FailErr(t, "bounded pattern page", err)
		res := decodeSummarizeResponse(t, raw)
		if res.Coverage.MatchesObserved != page+1 || observed.Orchestration.Curator.SourceBytesRead > 128 {
			t.Fatalf("pattern coverage=%+v work=%+v", res.Coverage, observed.Orchestration.Curator)
		}
		for _, row := range res.Pack.Identity {
			seen[row.Path] = true
		}
		if res.Coverage.NextCursor == "" {
			break
		}
		args = map[string]any{"path": ".", "pattern": "Needle", "cursor": res.Coverage.NextCursor}
	}
	if len(seen) != 3 {
		t.Fatalf("pattern detail visited %d files", len(seen))
	}
}
