package survey

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSummarizeGatherInlineChunking(t *testing.T) {
	caps := summarize.DefaultCaps()
	caps.Gather.InlineChunkLines = 5
	g := testSummarizeGatherer(t, t.TempDir(), caps)

	content := strings.TrimSpace(strings.Repeat("line\n", 12))
	res, err := g.Gather(context.Background(), summarize.Request{Content: content})
	testutil.FailErr(t, "gather", err)
	if len(res.Candidates) != 3 {
		t.Fatalf("candidates = %d, want 3 inline chunks", len(res.Candidates))
	}
	if res.Candidates[0].RelPath != "inline#1" || res.Candidates[2].RelPath != "inline#3" {
		t.Fatalf("labels = %q %q %q", res.Candidates[0].RelPath, res.Candidates[1].RelPath, res.Candidates[2].RelPath)
	}
	if res.Stats.Mode != summarize.ModeInline {
		t.Fatalf("mode = %q", res.Stats.Mode)
	}
}

func TestSummarizeGatherPatternGrouping(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pkg/a.go", "package main\nfunc Alpha() {}\n")
	writeFile(t, dir, "pkg/b.go", "package main\nfunc Beta() {}\n")
	writeFile(t, dir, "pkg/c.go", "package other\n")

	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	res, err := g.Gather(context.Background(), summarize.Request{
		Path: "pkg", Pattern: "func",
	})
	testutil.FailErr(t, "gather", err)
	paths := structurePaths(res.Structure)
	if len(paths) != 2 {
		t.Fatalf("paths = %v, want one structural candidate per matching file", paths)
	}
	if !containsPath(paths, "pkg/a.go") || !containsPath(paths, "pkg/b.go") {
		t.Fatalf("unexpected paths %v", paths)
	}
	if len(res.SampleMatches) < 2 {
		t.Fatalf("SampleMatches = %+v, want ≥2 hit lines", res.SampleMatches)
	}
	if !res.Stats.HasPattern || !res.Stats.UseStructure {
		t.Fatal("expected HasPattern and UseStructure")
	}
}

func TestSummarizeGatherPatternSampleMatchesPrefixSemantics(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "auth.go", "package auth\nvar _ http.Handler\nvar _ http.HandlerFunc\n")
	writeFile(t, dir, "routes.go", "package routes\nfunc init() { http.Handle(\"/\", nil) }\n")

	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	res, err := g.Gather(context.Background(), summarize.Request{
		Path: ".", Pattern: `http\.Handle`,
	})
	testutil.FailErr(t, "gather", err)
	if len(res.SampleMatches) == 0 {
		t.Fatal("expected sample_matches from patterned gather")
	}
	joined := ""
	for _, s := range res.SampleMatches {
		joined += s.Path + ":" + s.Content + "\n"
	}
	if !strings.Contains(joined, "Handler") {
		t.Fatalf("sample_matches = %+v, want Handler prefix matches visible", res.SampleMatches)
	}
}

func TestSummarizeGatherPatternScopesHeadAndHash(t *testing.T) {
	dir := t.TempDir()
	// Far-apart symbols: only Replace should survive pattern scoping.
	body := "package main\n\nfunc Keep() {}\n" + strings.Repeat("// pad\n", 80) + "func Replace() {}\n"
	writeFile(t, dir, "pkg/edit.go", body)

	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	bare, err := g.Gather(context.Background(), summarize.Request{Path: "pkg/edit.go"})
	testutil.FailErr(t, "bare gather", err)
	pat, err := g.Gather(context.Background(), summarize.Request{Path: "pkg/edit.go", Pattern: "Replace"})
	testutil.FailErr(t, "pattern gather", err)
	if len(bare.Structure) != 1 || len(pat.Structure) != 1 {
		t.Fatalf("bare=%d pat=%d structure", len(bare.Structure), len(pat.Structure))
	}
	if bare.Structure[0].ContentHash == pat.Structure[0].ContentHash {
		t.Fatal("pattern-scoped structure must change ContentHash vs bare path")
	}
	if pat.MatchCount < 1 {
		t.Fatalf("MatchCount = %d, want ≥1", pat.MatchCount)
	}
	hasReplace := false
	matchRows := 0
	for _, s := range pat.Structure[0].Symbols {
		if s.Kind == "match" {
			matchRows++
		}
		if strings.Contains(s.Name, "Replace") {
			hasReplace = true
		}
		if s.Name == "Keep" || strings.Contains(s.Name, "func Keep") {
			t.Fatalf("Keep should be outside match window; got %v", pat.Structure[0].Symbols)
		}
	}
	if matchRows < 1 {
		t.Fatalf("expected match-line symbols; got %v", pat.Structure[0].Symbols)
	}
	if !hasReplace {
		t.Fatalf("pattern symbols = %v, want Replace", pat.Structure[0].Symbols)
	}
	if !strings.Contains(pat.Structure[0].Head, "Replace") {
		t.Fatalf("pattern head = %q, want Replace window", pat.Structure[0].Head)
	}
}

func TestSummarizeGatherPatternNoMatchSamples(t *testing.T) {
	dir := t.TempDir()
	// Method receivers — a bare "func handle[A-Z]" pattern does not match.
	writeFile(t, dir, "pkg/server.go", "package main\n\nfunc (s *Server) handleSessions() {}\nfunc (s *Server) handleWorkflowRuns() {}\nfunc NewServer() {}\n")

	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	_, err := g.Gather(context.Background(), summarize.Request{
		Path: "pkg/server.go", Pattern: `func handle[A-Z]`,
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SUMMARIZE_NO_MATERIAL" {
		t.Fatalf("err = %v, want SUMMARIZE_NO_MATERIAL", err)
	}
	if reject.Data["match_count"] != 0 {
		t.Fatalf("match_count = %v", reject.Data["match_count"])
	}
	if reject.Data["pattern"] != `func handle[A-Z]` {
		t.Fatalf("pattern = %v", reject.Data["pattern"])
	}
	samples, _ := reject.Data["sample_identifiers"].(string)
	if !strings.Contains(samples, "handleSessions") || !strings.Contains(samples, "handleWorkflowRuns") {
		t.Fatalf("sample_identifiers = %q, want outline handler names", samples)
	}
}

func TestSummarizeGatherPatternCountsAllMatchesAndSamplesFile(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("package main\n\nfunc routes() {\n")
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, "\thandleRoute%d()\n", i)
	}
	b.WriteString("}\n")
	writeFile(t, dir, "pkg/routes.go", b.String())

	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	res, err := g.Gather(context.Background(), summarize.Request{
		Path: "pkg/routes.go", Pattern: `handleRoute`,
	})
	testutil.FailErr(t, "pattern gather", err)
	if res.MatchCount != 20 {
		t.Fatalf("MatchCount = %d, want 20", res.MatchCount)
	}
	if len(res.Structure) != 1 {
		t.Fatalf("structure = %d", len(res.Structure))
	}
	matchRows := 0
	for _, s := range res.Structure[0].Symbols {
		if s.Kind == "match" {
			matchRows++
		}
	}
	if matchRows != 1 {
		t.Fatalf("match symbols = %d, want one file sample", matchRows)
	}
}

func TestSummarizeGatherByteBudgetDefersToSubtree(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "big/a.go", strings.Repeat("x", 500))
	writeFile(t, dir, "big/b.go", strings.Repeat("y", 500))

	caps := summarize.DefaultCaps()
	caps.Gather.MaxFilesRead = 0 // disable file cap
	caps.Gather.MaxBytes = 500
	g := testSummarizeGatherer(t, dir, caps)

	res, err := g.Gather(context.Background(), summarize.Request{Paths: []string{"big/a.go", "big/b.go"}})
	testutil.FailErr(t, "gather", err)
	if len(structureFilePaths(res.Structure)) != 1 {
		t.Fatalf("structure files = %d, want byte cap to stop after one file", len(structureFilePaths(res.Structure)))
	}
	if res.Subtree == nil || res.Subtree.Material.SourceFiles != 2 {
		t.Fatalf("subtree = %+v, want both files", res.Subtree)
	}
}

func TestSummarizeGatherPatternDeduplicatesOverlappingPaths(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pkg/a.go", "package pkg\nfunc Match() {}\n")
	writeFile(t, dir, "pkg/b.go", "package pkg\nfunc Other() {}\n")

	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	res, err := g.Gather(context.Background(), summarize.Request{
		Paths: []string{"pkg", "pkg/a.go"}, Pattern: "func Match",
	})
	testutil.FailErr(t, "gather", err)
	if res.MatchCount != 1 || res.MatchingFilesObserved != 1 {
		t.Fatalf("matches=%d files=%d, want one unique match", res.MatchCount, res.MatchingFilesObserved)
	}
}

func TestSummarizeGatherBoundaryReject(t *testing.T) {
	dir := t.TempDir()
	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	_, err := g.Gather(context.Background(), summarize.Request{Path: "../outside"})
	var reject *toolrejection.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "SURVEY_PATH_ESCAPE" {
		t.Fatalf("err = %v, want SURVEY_PATH_ESCAPE", err)
	}
}

func TestSummarizeGatherHybridMode(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pkg/a.go", "package main\n")
	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	res, err := g.Gather(context.Background(), summarize.Request{
		Content: strings.Repeat("inline\n", 30),
		Path:    "pkg/a.go",
	})
	testutil.FailErr(t, "gather", err)
	if res.Stats.Mode != summarize.ModeHybrid {
		t.Fatalf("mode = %q, want hybrid", res.Stats.Mode)
	}
	inline := 0
	for _, c := range res.Candidates {
		if c.Kind == summarize.KindInline {
			inline++
		}
	}
	if inline == 0 {
		t.Fatal("expected inline candidates in hybrid gather")
	}
	if len(structureFilePaths(res.Structure)) == 0 {
		t.Fatal("expected repo structure in hybrid gather")
	}
}

func structurePaths(structure []summarize.StructureCandidate) []string {
	var out []string
	for _, s := range structure {
		if s.Kind == summarize.StructureKindFile {
			out = append(out, s.RelPath)
		}
	}
	return out
}

func structureFilePaths(structure []summarize.StructureCandidate) []string {
	return structurePaths(structure)
}

func structureHasDirMap(structure []summarize.StructureCandidate) bool {
	for _, s := range structure {
		if s.Kind == summarize.StructureKindDirMap {
			return true
		}
	}
	return false
}

func TestSummarizeGatherLargeDirUsesStructure(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"a", "b", "c"} {
		for i := 0; i < 25; i++ {
			writeFile(t, dir, fmt.Sprintf("nest/%s/f%d.go", sub, i), "package main\n")
		}
	}
	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())

	res, err := g.Gather(context.Background(), summarize.Request{Path: "nest"})
	testutil.FailErr(t, "gather", err)
	if len(structureFilePaths(res.Structure)) != 0 {
		t.Fatalf("directory gather should outline on demand, got %v", structureFilePaths(res.Structure))
	}
	if !structureHasDirMap(res.Structure) {
		t.Fatal("expected directory map")
	}
	if res.Subtree == nil {
		t.Fatal("expected material subtree")
	}
	if !res.Stats.UseStructure {
		t.Fatal("expected UseStructure")
	}
	if !res.Stats.PathIsDir {
		t.Fatal("expected PathIsDir")
	}
}

func TestSummarizeGatherFlatLargeDirUsesStructure(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 70; i++ {
		writeFile(t, dir, fmt.Sprintf("big/f%d.go", i), "package main\n")
	}
	writeFile(t, dir, "big/README.md", "# big dir\n")
	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())

	res, err := g.Gather(context.Background(), summarize.Request{Path: "big"})
	testutil.FailErr(t, "gather", err)
	if len(res.Candidates) != 0 {
		t.Fatalf("structure gather should not leaf files, got %d candidates", len(res.Candidates))
	}
	if len(structureFilePaths(res.Structure)) != 0 {
		t.Fatalf("directory gather should outline on demand, got %v", structureFilePaths(res.Structure))
	}
	if !structureHasDirMap(res.Structure) {
		t.Fatal("expected directory map")
	}
	if res.Subtree == nil {
		t.Fatal("expected material subtree")
	}
	if !res.Stats.UseStructure {
		t.Fatal("expected UseStructure")
	}
}

func TestSummarizeGatherPathsRejectsPartialMissing(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "# hi\n")
	writeFile(t, dir, "pkg/a.go", "package main\n")
	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())

	_, err := g.Gather(context.Background(), summarize.Request{
		Paths: []string{"README.md", "go.mod", "pkg/a.go"},
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SUMMARIZE_PATHS_MISSING" {
		t.Fatalf("err = %v, want SUMMARIZE_PATHS_MISSING", err)
	}
}

func TestSummarizeGatherPathsAllMissing(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "# hi\n")
	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())

	_, err := g.Gather(context.Background(), summarize.Request{
		Paths: []string{"go.mod", "package.json"},
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SUMMARIZE_PATHS_MISSING" {
		t.Fatalf("err = %v, want SUMMARIZE_PATHS_MISSING", err)
	}
}

func TestSummarizeGatherSinglePathMissing(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "# hi\n")
	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())

	_, err := g.Gather(context.Background(), summarize.Request{Path: "ghost/missing.go"})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SUMMARIZE_PATHS_MISSING" {
		t.Fatalf("err = %v, want SUMMARIZE_PATHS_MISSING", err)
	}
	missing, _ := reject.Data["missing_paths"].([]string)
	if len(missing) != 1 || missing[0] != "ghost/missing.go" {
		t.Fatalf("missing = %v, want [ghost/missing.go]", reject.Data["missing_paths"])
	}
}

func TestSummarizeGatherPackageJSONNestedKeys(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{
  "name": "demo",
  "scripts": {"test": "vitest"},
  "dependencies": {"solid-js": "^1.0.0", "vite": "^5.0.0"},
  "devDependencies": {"typescript": "^5.0.0"}
}`)
	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	res, err := g.Gather(context.Background(), summarize.Request{Path: "package.json"})
	testutil.FailErr(t, "gather", err)
	if len(res.Structure) != 1 {
		t.Fatalf("structure = %d, want 1", len(res.Structure))
	}
	names := map[string]bool{}
	for _, s := range res.Structure[0].Symbols {
		names[s.Name] = true
	}
	for _, want := range []string{"dependencies", "devDependencies", "dependencies.solid-js", "dependencies.vite", "devDependencies.typescript"} {
		if !names[want] {
			t.Fatalf("symbol %q missing from %v", want, names)
		}
	}
}

func TestSummarizeGatherSmallDirUsesStructure(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		writeFile(t, dir, fmt.Sprintf("small/f%d.go", i), "package main\n")
	}
	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	res, err := g.Gather(context.Background(), summarize.Request{Path: "small"})
	testutil.FailErr(t, "gather", err)
	if len(structureFilePaths(res.Structure)) != 0 {
		t.Fatalf("directory gather should outline on demand, got %v", structureFilePaths(res.Structure))
	}
	if !structureHasDirMap(res.Structure) {
		t.Fatal("expected dir_map structure candidate")
	}
	if !res.Stats.UseStructure {
		t.Fatal("expected UseStructure")
	}
}

func TestSummarizeGatherAttachesSubtree(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pkg/a.go", "package pkg\nfunc Alpha() {}\n")
	writeFile(t, dir, "pkg/b.go", "package pkg\nfunc Beta() {}\n")
	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())

	res, err := g.Gather(context.Background(), summarize.Request{Path: "pkg"})
	testutil.FailErr(t, "gather", err)
	if res.Subtree == nil {
		t.Fatal("expected Subtree on dir gather")
	}
	if res.Subtree.Kind != summarize.SubtreeKindDir {
		t.Fatalf("kind = %q", res.Subtree.Kind)
	}
	if res.Subtree.Material.Defs <= 0 && res.Subtree.Material.SourceFiles <= 0 {
		t.Fatalf("empty material: %+v", res.Subtree.Material)
	}

	fileRes, err := g.Gather(context.Background(), summarize.Request{Path: "pkg/a.go"})
	testutil.FailErr(t, "file gather", err)
	if fileRes.Subtree == nil || fileRes.Subtree.Kind != summarize.SubtreeKindFile {
		t.Fatalf("file subtree = %+v", fileRes.Subtree)
	}

	pat, err := g.Gather(context.Background(), summarize.Request{Path: "pkg", Pattern: "func"})
	testutil.FailErr(t, "pattern gather", err)
	if pat.Subtree != nil {
		t.Fatal("pattern gather must not attach Subtree")
	}
}

func TestSummarizeGatherSubtreeIncludesIndexedVendorFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "src/main.go", "package main\nfunc Main() {}\n")
	writeFile(t, dir, "node_modules/leftpad/index.js", "module.exports = 1\n")
	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())

	res, err := g.Gather(context.Background(), summarize.Request{Path: "."})
	testutil.FailErr(t, "gather", err)
	if res.Subtree == nil {
		t.Fatal("expected Subtree")
	}
	sawSource := false
	sawVendor := false
	var walk func(*summarize.SubtreeNode)
	walk = func(n *summarize.SubtreeNode) {
		if n == nil {
			return
		}
		if n.LoadChildren != nil {
			n.LoadChildren(context.Background(), n)
		}
		if strings.Contains(n.Path, "node_modules") {
			sawVendor = true
		}
		if n.Path == "src/main.go" {
			sawSource = true
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(res.Subtree)
	if !sawSource {
		t.Fatal("source path absent from subtree")
	}
	if !sawVendor {
		t.Fatal("indexed vendor path absent from subtree")
	}
}

func TestSummarizeStreamsFileStructureInBoundedChunks(t *testing.T) {
	dir := t.TempDir()
	largePath := filepath.Join(dir, "large.go")
	body := "package main\nfunc First() {}\n" + strings.Repeat("// padding\n", 300) + "func Last() {}\n"
	if err := os.WriteFile(largePath, []byte(body), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	caps := summarize.DefaultCaps()
	caps.Gather.FileChunkBytes = 1024
	g := testSummarizeGatherer(t, dir, caps)
	sc, _, ok := g.sources.structureFromAbs(context.Background(), largePath, "large.go")
	if !ok {
		t.Fatal("streamed structure was unavailable")
	}
	if sc.LineCount != strings.Count(body, "\n")+1 {
		t.Fatalf("line count = %d", sc.LineCount)
	}
	foundLast := false
	for _, symbol := range sc.Symbols {
		if symbol.Name == "Last" {
			foundLast = true
		}
	}
	if !foundLast {
		t.Fatalf("tail symbol missing: %+v", sc.Symbols)
	}
}

func TestSummarizePatternPagesEveryIndexedTextFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".hidden/hit.txt", "Needle\n")
	writeFile(t, dir, "ignored/hit.txt", "Needle\n")
	writeFile(t, dir, ".gitignore", "ignored/\n")
	writeFile(t, dir, "tail.txt", strings.Repeat("padding\n", 300)+"Needle\n")
	tool := testSummarizeTool(t, dir)
	tool.Caps.Gather.FileChunkBytes = 1024
	tool.Caps.Gather.PatternPageFiles = 1
	prepareBriefingCatalog(t, dir, tool)
	args := map[string]any{"path": ".", "pattern": "Needle"}
	seen := map[string]bool{}
	for page := 0; page < 8; page++ {
		raw, err := tool.Run(t.Context(), args, nativefixture.Context(dir))
		testutil.FailErr(t, "gather pattern page", err)
		result := decodeSummarizeResponse(t, raw)
		for _, source := range result.Pack.Identity {
			if seen[source.Path] {
				t.Fatalf("repeated matching file %s", source.Path)
			}
			seen[source.Path] = true
		}
		if result.Coverage.NextCursor == "" {
			if result.Coverage.MatchesObserved != 3 || result.Coverage.MatchingFilesObserved != 3 {
				t.Fatalf("pattern coverage=%+v", result.Coverage)
			}
			break
		}
		args = map[string]any{"path": ".", "pattern": "Needle", "cursor": result.Coverage.NextCursor}
	}
	if len(seen) != 3 {
		t.Fatalf("matching files=%v", seen)
	}
}

func TestSummarizePatternBoundsSingleLineCapture(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "minified.txt", strings.Repeat("x", 128<<10)+" Needle")

	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	res, err := g.Gather(context.Background(), summarize.Request{Path: ".", Pattern: "Needle"})
	testutil.FailErr(t, "gather pattern", err)
	if res.MatchCount != 1 || len(res.SampleMatches) != 1 {
		t.Fatalf("matches=%d samples=%d", res.MatchCount, len(res.SampleMatches))
	}
	if !strings.Contains(res.SampleMatches[0].Content, "Needle") {
		t.Fatalf("sample = %q", res.SampleMatches[0].Content)
	}
	if len(res.SampleMatches[0].Content) > patternMatchSampleLineMax {
		t.Fatalf("sample bytes = %d", len(res.SampleMatches[0].Content))
	}
}

func TestSummarizePatternStopsAtSourceBudgetOnWideLine(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "large.txt", strings.Repeat("x", 2<<20)+" Needle")
	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	result, err := g.Gather(t.Context(), summarize.Request{Path: "large.txt", Pattern: "Needle"})
	testutil.FailErr(t, "bounded wide pattern source", err)
	if result.MatchCount != 0 || !g.sources.sourceLimited || g.sources.sourceReadBytes > int64(g.caps.Gather.FileReadBytes) {
		t.Fatalf("pattern read=%d matches=%d limited=%v", g.sources.sourceReadBytes, result.MatchCount, g.sources.sourceLimited)
	}
}
