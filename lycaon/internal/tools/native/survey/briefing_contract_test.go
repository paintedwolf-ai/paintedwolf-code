package survey

import (
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
	"strings"
	"testing"
	"time"
)

func prepareBriefingCatalog(t *testing.T, dir string, tool *SummarizeTool) {
	t.Helper()
	if tool.Catalog == nil {
		tool.Catalog = sourcecatalog.New()
	}
	t.Cleanup(func() { testutil.FailErr(t, "drain briefing index", tool.Catalog.Drain(t.Context())) })
	tctx := nativefixture.Context(dir)
	resolved, err := projectpaths.ResolveRead(t.Context(), tool.Boundary, tctx, ".")
	testutil.FailErr(t, "resolve briefing root", err)
	scope, err := tool.Boundary.CompileReadScope(t.Context(), dir, tctx.ProfileID())
	testutil.FailErr(t, "compile briefing scope", err)
	reader, status, err := tool.Catalog.Trees.OpenSummary(t.Context(), tctx.Identity.ProjectID, sourcecatalog.Root{ID: resolved.Root.ID, Path: dir}, sourcecatalog.TreeScope{Key: scope.Key, Filter: scope.Filter, PruneNestedVCS: true}, 30*time.Second)
	testutil.FailErr(t, "prepare briefing catalog", err)
	if reader == nil {
		t.Fatalf("catalog not ready: %+v", status)
	}
	testutil.FailErr(t, "close preparation view", reader.Close())
}

func TestDirectoryMapWorkAndPages(t *testing.T) {
	for _, size := range []int{32, 256} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			dir := t.TempDir()
			for i := range size {
				writeFile(t, dir, fmt.Sprintf("area-%03d-%s/deep/file.go", i, strings.Repeat("x", 100)), "package source\n")
			}
			summary := testSummarizeTool(t, dir)
			prepareBriefingCatalog(t, dir, summary)
			var work mapWork
			tool := &ListDirTool{Boundary: summary.Boundary, Catalog: summary.Catalog, ObserveMap: func(w mapWork) { work = w }}
			args := map[string]any{"path": "."}
			seen := map[string]bool{}
			for page := 0; page < size; page++ {
				raw, err := tool.Run(t.Context(), args, nativefixture.Context(dir))
				testutil.FailErr(t, "map page", err)
				var result listDirResponse
				testutil.FailErr(t, "decode map page", json.Unmarshal([]byte(surveyJSONBody(raw)), &result))
				receipt, ok := surveyreceipt.Parse(raw)
				if !ok || receipt.Truncated != result.Truncated {
					t.Fatalf("map receipt=%+v truncated=%v", receipt, result.Truncated)
				}
				if len(raw) > mapTokenBudget*4 || work.MetadataRows > mapPageEntries*3+1 || work.DirectoriesOpened != 0 || work.DirectoryEntries != 0 {
					t.Fatalf("map cost: bytes=%d work=%+v", len(raw), work)
				}
				if result.Coverage.EntriesTotal == nil || *result.Coverage.EntriesTotal != size {
					t.Fatalf("map coverage=%+v", result.Coverage)
				}
				for _, child := range result.Tree.Children {
					if seen[child.Path] || child.DescendantFiles == nil || *child.DescendantFiles != 1 || len(child.Children) != 0 {
						t.Fatalf("map child=%+v repeated=%v", child, seen[child.Path])
					}
					seen[child.Path] = true
				}
				if len(result.NextActions) == 0 {
					break
				}
				action := result.NextActions[0]
				args = map[string]any{"path": action.Path, "cursor": action.Cursor}
			}
			if len(seen) != size {
				t.Fatalf("map visited=%d want=%d", len(seen), size)
			}
		})
	}
}

func TestPatternBriefingWorkBoundIncludesDiscovery(t *testing.T) {
	for _, size := range []int{33, 129} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			dir := t.TempDir()
			for i := range size {
				writeFile(t, dir, fmt.Sprintf("file-%03d.txt", i), strings.Repeat("ordinary content\n", 30))
			}
			writeFile(t, dir, "zzz.txt", "Needle with useful evidence\n")
			tool := testSummarizeTool(t, dir)
			tool.Caps.Gather.MaxFilesRead = 4
			tool.Caps.Gather.FileReadBytes = 128
			tool.Caps.Gather.MaxBytes = 512
			prepareBriefingCatalog(t, dir, tool)
			var observed summarize.Result
			tool.Observe = func(result summarize.Result) { observed = result }
			args := map[string]any{"path": ".", "pattern": "Needle"}
			var result summarizeResponse
			for page := 0; page < size; page++ {
				raw, err := tool.Run(t.Context(), args, nativefixture.Context(dir))
				testutil.FailErr(t, "pattern work page", err)
				result = decodeSummarizeResponse(t, raw)
				work := observed.Orchestration.Curator
				if work.SourceFilesRead > 4 || work.SourceBytesRead > 512 || work.MetadataRowsRead > 5 || tool.Caps.EstimateTokens(raw) > tool.Caps.Pack.WireBudgetTokens {
					t.Fatalf("pattern work=%+v", work)
				}
				if strings.Contains(raw, "\"orchestration\"") || strings.Contains(raw, "\"gaps\"") {
					t.Fatal("host diagnostics leaked into briefing")
				}
				if result.Coverage.NextCursor == "" {
					break
				}
				args = map[string]any{"path": ".", "pattern": "Needle", "cursor": result.Coverage.NextCursor}
			}
			if result.Coverage.MatchesObserved != 1 || result.Coverage.MatchingFilesObserved != 1 || len(result.Anchors) == 0 {
				t.Fatalf("pattern final page=%+v", result)
			}
		})
	}
}

func TestColdDirectoryMapHasOneBoundedDirectoryRead(t *testing.T) {
	dir := t.TempDir()
	for i := range 100 {
		writeFile(t, dir, fmt.Sprintf("area-%03d/deep/source.go", i), "package source")
	}
	tctx := nativefixture.Context(dir)
	tool := &ListDirTool{Boundary: nativefixture.Boundary(t)}
	resolved, err := projectpaths.ResolveRead(t.Context(), tool.Boundary, tctx, ".")
	testutil.FailErr(t, "resolve cold map", err)
	result := listDirResponse{Path: ".", View: "map", Tree: &directoryMapNode{Path: ".", Type: "dir"}, Coverage: &mapCoverage{CatalogState: "warming"}}
	var work mapWork
	testutil.FailErr(t, "cold map", coldDirectoryMap(t.Context(), tool, tctx, resolved, &result, &work))
	if work.DirectoriesOpened != 1 || work.DirectoryEntries > mapReadEntries || len(result.Tree.Children) > mapPageEntries {
		t.Fatalf("cold work=%+v", work)
	}
	for _, node := range result.Tree.Children {
		if node.ImmediateChildren != nil || node.DescendantFiles != nil {
			t.Fatalf("unobserved counts=%+v", node)
		}
	}
}

func TestBriefingDoesNotSuggestObservedNonTextSource(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "metadata.any", "\x00\x01\x02opaque bytes")
	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	if _, ok := g.sources.Outline(t.Context(), "metadata.any"); ok {
		t.Fatal("binary source accepted as text")
	}
	actions := g.sources.actionableSources([]summarize.NextAction{{Tool: "summarize", Path: "metadata.any"}, {Tool: "summarize", Path: "src"}})
	if len(actions) != 1 || actions[0].Path != "src" {
		t.Fatalf("source actions=%+v", actions)
	}
}

func TestBriefingRetainsLateDefinitionAcrossReadStrategies(t *testing.T) {
	for _, chunkBytes := range []int{256, 1 << 20} {
		t.Run(fmt.Sprint(chunkBytes), func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "principal.go", "package principal\n"+strings.Repeat("// context\n", 100)+"func Subsumes() bool {\n return true\n}\n")
			tool := testSummarizeTool(t, dir)
			tool.Caps.Gather.FileChunkBytes = chunkBytes
			tool.Caps.Gather.FileHeadLines = 20
			raw, err := tool.Run(t.Context(), map[string]any{"path": "principal.go", "task": "How does Subsumes work?"}, nativefixture.Context(dir))
			testutil.FailErr(t, "late definition briefing", err)
			result := decodeSummarizeResponse(t, raw)
			found := false
			for _, window := range result.Pack.Substance {
				found = found || strings.Contains(window.Body, "return true")
			}
			if !found {
				t.Fatalf("late definition missing: %s", raw)
			}
		})
	}
}
