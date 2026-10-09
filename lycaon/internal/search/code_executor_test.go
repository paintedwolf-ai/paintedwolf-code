package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourcescope"
	catalogtest "github.com/lycaon/lycaon/internal/testsetup/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

// codeFixtureRoot lays out a small tree: first-party source, a vendored copy
// under node_modules, and a build output under dist.
func codeFixtureRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"cmd/server/main.go":            "package main\n\nfunc main() {}\n",
		"internal/search/toolbar.go":    "package search\n\n// toolbar front door\n",
		"node_modules/toolbar/index.js": "module.exports = {}\n",
		"dist/toolbar.js":               "// built bundle\n",
		// This fixture matches only by filename.
		"docs/toolbar.md": "# Front door\n\nOverlay panel.\n",
	}
	for rel, body := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			testutil.FailErr(t, "mkdir fixture dir", err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			testutil.FailErr(t, "write fixture file", err)
		}
	}
	return root
}

func runCodeLeg(t *testing.T, leg *CodePlanLeg) []Hit {
	t.Helper()
	for _, root := range leg.PathRoots {
		testutil.FailErr(t, "prepare source inventory", catalogtest.AwaitIndex(t.Context(), sourcecatalog.Process(), root.ProjectID,
			sourcecatalog.Root{ID: root.RootID, Path: root.Path}))
	}
	report, err := NewCodeExecutor(decide.Reranker{}).Run(context.Background(), PlanLeg{Code: leg})
	if err != nil {
		testutil.FailErr(t, "run code leg", err)
	}
	return report.Hits
}

func hitPathsOfKind(hits []Hit, kind string) []string {
	var out []string
	for _, h := range hits {
		if h.HitKind == kind {
			out = append(out, h.Path)
		}
	}
	return out
}

func TestCodeExecutorKeepsEqualPathsInDistinctRoots(t *testing.T) {
	roots := []CodeRoot{
		{ProjectID: "p", RootID: "left", Path: t.TempDir()},
		{ProjectID: "p", RootID: "right", Path: t.TempDir()},
	}
	for _, root := range roots {
		testutil.FailErr(t, "write root fixture", os.WriteFile(filepath.Join(root.Path, "needle.txt"), []byte("needle\n"), 0o600))
	}
	hits := runCodeLeg(t, &CodePlanLeg{Query: TextExpr{Text: "needle"}, PathRoots: roots, Files: true, Lines: true})
	seen := map[string]bool{}
	for _, hit := range hits {
		if hit.RootID == "" || hit.Path != "needle.txt" || seen[hit.ID] {
			t.Fatalf("ambiguous source identity: %+v", hit)
		}
		seen[hit.ID] = true
	}
	if len(hits) != 4 {
		t.Fatalf("root hits = %+v, want file and line in each root", hits)
	}
}

func TestCodeExecutorRequiresRegisteredRootID(t *testing.T) {
	_, err := NewCodeExecutor(decide.Reranker{}).Run(t.Context(), PlanLeg{Code: &CodePlanLeg{
		Query: TextExpr{Text: "needle"}, PathRoots: []CodeRoot{{ProjectID: "p1", Path: t.TempDir()}},
		Files: true,
	}})
	if err == nil || !strings.Contains(err.Error(), "root ID is required") {
		t.Fatalf("missing root identity: %v", err)
	}
}

func TestCodeExecutorDecodesSelfIdentifyingEncodings(t *testing.T) {
	for _, encoding := range testutil.SelfIdentifyingTextEncodings() {
		t.Run(encoding, func(t *testing.T) {
			root := t.TempDir()
			raw := testutil.EncodeTextFixture(t, "first\nencoding_target\n", encoding)
			testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(root, "document.txt"), raw, 0o644))
			hits := runCodeLeg(t, &CodePlanLeg{
				Query: TextExpr{Text: "encoding_target"}, PathRoots: []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
				Lines: true, Cap: SearchExecutorProbeHits,
			})
			if len(hits) != 1 || hits[0].Path != "document.txt" || hits[0].Line != 2 ||
				hits[0].Snippet != "encoding_target" {
				t.Fatalf("hits = %+v", hits)
			}
		})
	}
}

func TestCodeDocumentReadsPastStaleCatalogSize(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, "growing.txt")
	testutil.FailErr(t, "write cataloged prefix", os.WriteFile(abs, []byte("prefix\n"), 0o644))
	snapshot := sourcecatalog.Snapshot{
		State: sourcecatalog.StateReady,
		Entries: []sourcecatalog.Entry{{
			RootID: "root", Path: "growing.txt", Size: int64(len("prefix\n")),
		}},
	}
	files := codeFilesFromEntries(CodeRoot{ProjectID: "p1", RootID: "root", Path: root}, "root", root, snapshot.Entries)
	if len(files) != 1 {
		t.Fatalf("catalog files = %+v", files)
	}
	f, err := os.OpenFile(abs, os.O_APPEND|os.O_WRONLY, 0)
	testutil.FailErr(t, "open append", err)
	_, err = f.WriteString("appended needle\n")
	testutil.FailErr(t, "append content", err)
	testutil.FailErr(t, "close appended file", f.Close())
	doc, status := openCodeDocument(files[0].abs, codeExecutorMaxFileBytes)
	if status != codeOpenOK || !strings.Contains(doc.Text(), "appended needle") {
		t.Fatalf("document = %q, status = %v", doc.Text(), status)
	}
}

func TestCodeDocumentRejectsCurrentOversizeFile(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "oversize.txt")
	testutil.FailErr(t, "write oversized file", os.WriteFile(abs, bytes.Repeat([]byte{'x'}, 33), 0o644))
	if _, status := openCodeDocument(abs, 32); status != codeOpenUnsearchable {
		t.Fatalf("oversized current file: status = %v", status)
	}
}

func TestCodeDocumentRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	link := filepath.Join(root, "link.txt")
	testutil.FailErr(t, "write symlink target", os.WriteFile(target, []byte("needle\n"), 0o644))
	testutil.FailErr(t, "create symlink", os.Symlink(target, link))
	if _, status := openCodeDocument(link, codeExecutorMaxFileBytes); status == codeOpenOK {
		t.Fatal("symlink was accepted")
	}
}

func TestCodeExecutorEmitsFileHitsForPathMatches(t *testing.T) {
	root := codeFixtureRoot(t)
	hits := runCodeLeg(t, &CodePlanLeg{
		Query:     TextExpr{Text: "toolbar"},
		PathRoots: []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
		Files:     true,
		FileCap:   SearchExecutorProbeHits,
	})

	paths := hitPathsOfKind(hits, HitKindFile)
	// Name-only: "toolbar" is in the path, not the body.
	if !contains(paths, "docs/toolbar.md") {
		t.Fatalf("file hits missing docs/toolbar.md: %v", paths)
	}
	if !contains(paths, "internal/search/toolbar.go") {
		t.Fatalf("file hits missing internal/search/toolbar.go: %v", paths)
	}
	for _, h := range hits {
		if h.HitKind != HitKindFile {
			t.Fatalf("files-only leg emitted %q", h.HitKind)
		}
		if h.Line != 0 {
			t.Fatalf("file hit %q carries a line number", h.Path)
		}
		if h.Source != SourceCode {
			t.Fatalf("file hit %q source = %q", h.Path, h.Source)
		}
	}
}

func TestCodeExecutorFileHitsSkipDependencyAndBuildTrees(t *testing.T) {
	root := codeFixtureRoot(t)
	hits := runCodeLeg(t, &CodePlanLeg{
		Query:           TextExpr{Text: "toolbar"},
		PathRoots:       []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
		Files:           true,
		FileCap:         SearchExecutorProbeHits,
		FileExcludeDirs: []string{"node_modules", "dist", ".yarn/cache"},
	})

	for _, path := range hitPathsOfKind(hits, HitKindFile) {
		if path == "node_modules/toolbar/index.js" || path == "dist/toolbar.js" {
			t.Fatalf("navigation hit inside an excluded tree: %s", path)
		}
	}
	if !contains(hitPathsOfKind(hits, HitKindFile), "docs/toolbar.md") {
		t.Fatalf("excludes dropped first-party source")
	}
}

func TestCodeExecutorLineHitsHonorExplicitExclusions(t *testing.T) {
	root := codeFixtureRoot(t)
	testutil.FailErr(t, "write dependency match", os.WriteFile(
		filepath.Join(root, "node_modules/toolbar/index.js"), []byte("// toolbar dependency\n"), 0o644,
	))
	testutil.FailErr(t, "write build match", os.WriteFile(
		filepath.Join(root, "dist/toolbar.js"), []byte("// toolbar build\n"), 0o644,
	))
	hits := runCodeLeg(t, &CodePlanLeg{
		Query:           TextExpr{Text: "toolbar"},
		PathRoots:       []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
		Lines:           true,
		Cap:             SearchExecutorProbeHits,
		LineExcludeDirs: []string{"node_modules", "dist"},
	})
	paths := hitPathsOfKind(hits, HitKindCode)
	if !contains(paths, "internal/search/toolbar.go") {
		t.Fatalf("first-party content missing: %v", paths)
	}
	if contains(paths, "node_modules/toolbar/index.js") || contains(paths, "dist/toolbar.js") {
		t.Fatalf("excluded content leaked into definition search: %v", paths)
	}
}

// Excluded trees are sought past, not listed; siblings on both sides of the
// seek bound stay visible.
func TestCodeExecutorLinesSeekPastExcludedTrees(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir "+rel, os.MkdirAll(filepath.Dir(abs), 0o755))
		testutil.FailErr(t, "write "+rel, os.WriteFile(abs, []byte(body), 0o644))
	}
	for i := range sourcecatalog.TreeFilePageLimit + 88 {
		write(fmt.Sprintf("a/node_modules/pkg/%04d.js", i), "needle\n")
	}
	for _, rel := range []string{"a/node_modules-x.go", "a/node_modules0.go", "a/src/main.go", "b.go"} {
		write(rel, "needle\n")
	}
	testutil.FailErr(t, "prepare source inventory", catalogtest.AwaitIndex(t.Context(), sourcecatalog.Process(), "p1",
		sourcecatalog.Root{ID: "root", Path: root}))
	report, err := NewCodeExecutor(decide.Reranker{}).Run(context.Background(), PlanLeg{Code: &CodePlanLeg{
		Query:           TextExpr{Text: "needle"},
		PathRoots:       []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
		Lines:           true,
		Cap:             SearchExecutorProbeHits,
		LineExcludeDirs: []string{"node_modules"},
	}})
	testutil.FailErr(t, "run code leg", err)
	want := []string{"a/node_modules-x.go", "a/node_modules0.go", "a/src/main.go", "b.go"}
	paths := hitPathsOfKind(report.Hits, HitKindCode)
	slices.Sort(paths)
	if !slices.Equal(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	if report.Code.FilesListed != len(want) {
		t.Fatalf("listed %d files, want %d: the excluded tree was paged through", report.Code.FilesListed, len(want))
	}
}

func TestCodePathFilterPreservesUserCase(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write mixed-case file", os.WriteFile(filepath.Join(root, "README.md"), []byte("Needle\n"), 0o644))
	hits := runCodeLeg(t, &CodePlanLeg{
		Query: AndExpr{Exprs: []Node{
			FilterExpr{Field: "path", Value: "README.md"},
			TextExpr{Text: "Needle"},
		}},
		PathRoots: []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
		Lines:     true,
		Cap:       SearchExecutorProbeHits,
	})
	if paths := hitPathsOfKind(hits, HitKindCode); len(paths) != 1 || paths[0] != "README.md" {
		t.Fatalf("paths = %v, want README.md", paths)
	}
}

func TestCodeExecutorLinesOnlyLegKeepsContentBehavior(t *testing.T) {
	root := codeFixtureRoot(t)
	hits := runCodeLeg(t, &CodePlanLeg{
		Query:     TextExpr{Text: "toolbar"},
		PathRoots: []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
		Lines:     true,
		Cap:       SearchExecutorProbeHits,
	})

	if len(hitPathsOfKind(hits, HitKindFile)) != 0 {
		t.Fatalf("lines-only leg emitted file hits")
	}
	if !contains(hitPathsOfKind(hits, HitKindCode), "internal/search/toolbar.go") {
		t.Fatalf("content match lost: %v", hitPathsOfKind(hits, HitKindCode))
	}
	// Name-only fixture: body does not contain "toolbar".
	if contains(hitPathsOfKind(hits, HitKindCode), "docs/toolbar.md") {
		t.Fatalf("fixture no longer proves the name-only case")
	}
	for _, h := range hits {
		if h.HitKind == HitKindCode && h.Line == 0 {
			t.Fatalf("code hit %q lost its line number", h.Path)
		}
		if h.ID == "" {
			t.Fatalf("code hit %q has no stable id", h.Path)
		}
	}
}

func TestCodeExecutorFlaglessByteIdenticalSnapshot(t *testing.T) {
	root := codeFixtureRoot(t)
	base := &CodePlanLeg{
		Query:     TextExpr{Text: "toolbar"},
		PathRoots: []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
		Lines:     true,
		Files:     true,
		Cap:       SearchExecutorProbeHits,
		FileCap:   SearchExecutorProbeHits,
	}
	withEmpty := *base
	withEmpty.Flags = MatchFlags{}
	a := runCodeLeg(t, base)
	b := runCodeLeg(t, &withEmpty)
	flaglessSnapshot, err := json.Marshal(a)
	if err != nil {
		testutil.FailErr(t, "marshal flagless snapshot", err)
	}
	emptyFlagsSnapshot, err := json.Marshal(b)
	if err != nil {
		testutil.FailErr(t, "marshal explicit-empty-flags snapshot", err)
	}
	if !bytes.Equal(flaglessSnapshot, emptyFlagsSnapshot) {
		t.Fatalf("flagless snapshot differs from explicit empty flags:\nflagless: %s\nempty:    %s", flaglessSnapshot, emptyFlagsSnapshot)
	}
}

func TestCodeExecutorIncludeGlob(t *testing.T) {
	root := codeFixtureRoot(t)
	hits := runCodeLeg(t, &CodePlanLeg{
		Query:     TextExpr{Text: "toolbar"},
		PathRoots: []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
		Lines:     true,
		Cap:       SearchExecutorProbeHits,
		Flags:     MatchFlags{Include: []string{"**/*.go"}},
	})
	for _, path := range hitPathsOfKind(hits, HitKindCode) {
		if filepath.Ext(path) != ".go" {
			t.Fatalf("include leaked %q", path)
		}
	}
}

func TestCodeExecutorPreservesBooleanOr(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write alpha", os.WriteFile(filepath.Join(root, "alpha.txt"), []byte("alpha only\n"), 0o644))
	testutil.FailErr(t, "write beta", os.WriteFile(filepath.Join(root, "beta.txt"), []byte("beta only\n"), 0o644))
	hits := runCodeLeg(t, &CodePlanLeg{
		Query: OrExpr{Exprs: []Node{
			TextExpr{Text: "alpha"},
			TextExpr{Text: "beta"},
		}},
		PathRoots: []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
		Lines:     true,
		Cap:       SearchExecutorProbeHits,
	})
	if got := hitPathsOfKind(hits, HitKindCode); !contains(got, "alpha.txt") || !contains(got, "beta.txt") {
		t.Fatalf("OR hits = %v, want both files", got)
	}
}

func TestCodeExecutorEvaluatesPathFiltersInBooleanTree(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"src/alpha.txt", "docs/alpha.txt"} {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir path fixture", os.MkdirAll(filepath.Dir(abs), 0o755))
		testutil.FailErr(t, "write path fixture", os.WriteFile(abs, []byte("alpha\n"), 0o644))
	}

	hits := runCodeLeg(t, &CodePlanLeg{
		Query: AndExpr{Exprs: []Node{
			TextExpr{Text: "alpha"},
			NotExpr{Expr: FilterExpr{Field: "path", Value: "src/*"}},
		}},
		PathRoots: []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
		Lines:     true,
		Cap:       SearchExecutorProbeHits,
	})
	got := hitPathsOfKind(hits, HitKindCode)
	if contains(got, "src/alpha.txt") || !contains(got, "docs/alpha.txt") {
		t.Fatalf("path-filtered hits = %v, want docs only", got)
	}
}

func TestCodeExecutorOriginFirstFillsCap(t *testing.T) {
	origin := t.TempDir()
	other := t.TempDir()
	testutil.FailErr(t, "write other", os.WriteFile(filepath.Join(other, "a.txt"), []byte("needle\n"), 0o644))
	testutil.FailErr(t, "write origin", os.WriteFile(filepath.Join(origin, "z.txt"), []byte("needle\n"), 0o644))
	hits := runCodeLeg(t, &CodePlanLeg{
		Query: TextExpr{Text: "needle"},
		PathRoots: []CodeRoot{
			{ProjectID: "other", RootID: "other-root", Path: other},
			{ProjectID: "origin", RootID: "origin-root", Path: origin},
		},
		Lines: true,
		Cap:   1,
	})
	// PathRoots order is walk order. Cap 1 fills from the first root.
	if len(hits) != 1 || hits[0].ProjectID != "other" {
		t.Fatalf("ordered-root cap hits = %+v", hits)
	}

	ordered := orderCodeRoots([]CodeRoot{
		{ProjectID: "other", RootID: "other-root", Path: other},
		{ProjectID: "origin", RootID: "origin-root", Path: origin},
	}, "origin")
	hits = runCodeLeg(t, &CodePlanLeg{
		Query:     TextExpr{Text: "needle"},
		PathRoots: ordered,
		Lines:     true,
		Cap:       1,
	})
	if len(hits) != 1 || hits[0].ProjectID != "origin" {
		t.Fatalf("origin-first cap hits = %+v", hits)
	}
}

// Cached grams reject content candidates before opening files.
func TestCodeExecutorIndexNarrowsCandidatesBeforeOpening(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write hit", os.WriteFile(filepath.Join(root, "hit.txt"), []byte("unique_needle_token\n"), 0o644))
	testutil.FailErr(t, "write miss", os.WriteFile(filepath.Join(root, "miss.txt"), []byte("nothing interesting\n"), 0o644))
	catalog := sourcecatalog.Process()
	snapshot, err := catalog.Snapshot(t.Context(), "p1", []sourcecatalog.Root{{ID: "root", Path: root}})
	testutil.FailErr(t, "prepare metadata", err)
	_, err = catalog.Literals.LiteralCandidates(t.Context(), snapshot, sourcecatalog.LiteralQuery{RootID: "root", IncludeKey: "test-warm", Require: litprefilter.AnyOf("unique_target"), Open: func(entry sourcecatalog.Entry) (io.ReadCloser, error) {
		return os.Open(filepath.Join(root, entry.Path))
	}})
	testutil.FailErr(t, "prepare shared file observations", err)
	report, err := NewCodeExecutor(decide.Reranker{}).Run(context.Background(), PlanLeg{Code: &CodePlanLeg{
		Query:     TextExpr{Text: "unique_needle_token"},
		PathRoots: []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
		Lines:     true,
		Cap:       SearchExecutorProbeHits,
	}})
	testutil.FailErr(t, "run code leg", err)
	if hits := report.Hits; len(hits) != 1 || hits[0].Path != "hit.txt" {
		t.Fatalf("hits = %+v", hits)
	}
	code := report.Code
	if !code.IndexUsed || code.FilesListed != 2 || code.ContentCandidates != 1 || code.FilesOpened != 1 {
		t.Fatalf("code leg report = %+v, want one indexed candidate opened of two listed", code)
	}
}

// A file holding every gram of the literal but not the literal itself passes
// the index and is rejected by the byte prefilter before line matching.
func TestCodeExecutorPrefilterRejectsIndexFalsePositive(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write hit", os.WriteFile(filepath.Join(root, "hit.txt"), []byte("abcabc\n"), 0o644))
	testutil.FailErr(t, "write grams", os.WriteFile(filepath.Join(root, "grams.txt"), []byte("abc bca cab\n"), 0o644))
	report, err := NewCodeExecutor(decide.Reranker{}).Run(context.Background(), PlanLeg{Code: &CodePlanLeg{
		Query:     TextExpr{Text: "abcabc"},
		PathRoots: []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
		Lines:     true,
		Cap:       SearchExecutorProbeHits,
	}})
	testutil.FailErr(t, "run code leg", err)
	if hits := report.Hits; len(hits) != 1 || hits[0].Path != "hit.txt" {
		t.Fatalf("hits = %+v", hits)
	}
	if report.Code.ContentCandidates != 2 || report.Code.PrefilterSkipped != 1 {
		t.Fatalf("code leg report = %+v, want two candidates and one prefilter rejection", report.Code)
	}
}

// Both matcher families retain case-folded candidates.
func TestCodeExecutorCaseFoldsThroughIndex(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write upper", os.WriteFile(filepath.Join(root, "upper.txt"), []byte("FOLDED_NEEDLE here\n"), 0o644))
	testutil.FailErr(t, "write kelvin", os.WriteFile(filepath.Join(root, "kelvin.txt"), []byte("Kelvin\n"), 0o644))
	hits := runCodeLeg(t, &CodePlanLeg{
		Query:     AndExpr{Exprs: []Node{TextExpr{Text: "folded_needle"}}},
		PathRoots: []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
		Lines:     true,
		Cap:       SearchExecutorProbeHits,
	})
	if got := hitPathsOfKind(hits, HitKindCode); !contains(got, "upper.txt") {
		t.Fatalf("case-folded hits = %v, want upper.txt", got)
	}
	hits = runCodeLeg(t, &CodePlanLeg{
		Query:     TextExpr{Text: "kelvin"},
		PathRoots: []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
		Lines:     true,
		Cap:       SearchExecutorProbeHits,
	})
	if got := hitPathsOfKind(hits, HitKindCode); !contains(got, "kelvin.txt") {
		t.Fatalf("orbit-folded hits = %v, want kelvin.txt", got)
	}
}

// Invalidated observations force live reads under retained metadata.
func TestCodeExecutorFindsEditsUnderStaleGeneration(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, "edited.txt")
	testutil.FailErr(t, "write original", os.WriteFile(abs, []byte("original text\n"), 0o644))
	leg := &CodePlanLeg{
		Query:     TextExpr{Text: "fresh_needle"},
		PathRoots: []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
		Lines:     true,
		Cap:       SearchExecutorProbeHits,
	}
	if hits := runCodeLeg(t, leg); len(hits) != 0 {
		t.Fatalf("hits before edit = %+v", hits)
	}
	testutil.FailErr(t, "write edit", os.WriteFile(abs, []byte("now fresh_needle\n"), 0o644))
	sourcecatalog.Process().InvalidateRoot(root, "edited.txt")
	if got := hitPathsOfKind(runCodeLeg(t, leg), HitKindCode); !contains(got, "edited.txt") {
		t.Fatalf("hits after edit = %v, want edited.txt", got)
	}
}

func TestCodeExecutorReportsWarmingRootAfterJoinGrace(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o644))
	exec := NewCodeExecutor(decide.Reranker{})
	exec.generationJoinGrace = time.Nanosecond
	report, err := exec.Run(context.Background(), PlanLeg{Code: &CodePlanLeg{
		Query:     TextExpr{Text: "needle"},
		PathRoots: []CodeRoot{{ProjectID: "p1", RootID: "cold-root", Path: root}},
		Lines:     true,
		Cap:       SearchExecutorProbeHits,
	}})
	testutil.FailErr(t, "run code leg", err)
	if report.Code.WarmingRoots != 1 || report.TimedOut || len(report.Hits) != 0 {
		t.Fatalf("report = %+v, want one warming root and no timeout", report)
	}
}

// A warm root whose scan cannot finish inside the budget reports a timeout
// and keeps what it found.
func TestCodeExecutorTimeBudgetReportsPartial(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o644))
	leg := &CodePlanLeg{
		Query:     TextExpr{Text: "needle"},
		PathRoots: []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
		Lines:     true,
		Cap:       SearchExecutorProbeHits,
	}
	runCodeLeg(t, leg)
	exec := NewCodeExecutor(decide.Reranker{})
	exec.wallBudget = time.Nanosecond
	report, err := exec.Run(context.Background(), PlanLeg{Code: leg})
	testutil.FailErr(t, "run code leg", err)
	if !report.TimedOut {
		t.Fatalf("report = %+v, want a timed-out leg", report)
	}
}

func TestCodeLegWallReplacesBudgetClock(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o644))
	leg := &CodePlanLeg{
		Query:     TextExpr{Text: "needle"},
		PathRoots: []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
		Lines:     true,
		Cap:       SearchExecutorProbeHits,
	}
	runCodeLeg(t, leg)
	leg.Wall = time.Nanosecond
	report, err := NewCodeExecutor(decide.Reranker{}).Run(context.Background(), PlanLeg{Code: leg})
	testutil.FailErr(t, "run code leg", err)
	if !report.TimedOut {
		t.Fatalf("report = %+v, want the leg wall to bound the scan", report)
	}
}

func TestOversizeFileCountsAsSkipped(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write hit", os.WriteFile(filepath.Join(root, "hit.txt"), []byte("needle\n"), 0o644))
	big := append(bytes.Repeat([]byte("needle\n"), codeExecutorMaxFileBytes/7), []byte("needle\n")...)
	testutil.FailErr(t, "write oversize", os.WriteFile(filepath.Join(root, "big.txt"), big, 0o644))
	report, err := NewCodeExecutor(decide.Reranker{}).Run(context.Background(), PlanLeg{Code: &CodePlanLeg{
		Query:     TextExpr{Text: "needle"},
		PathRoots: []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
		Lines:     true,
		Cap:       SearchExecutorProbeHits,
	}})
	testutil.FailErr(t, "run code leg", err)
	if report.SkippedFiles != 1 || report.Code.FilesOpened != 2 {
		t.Fatalf("report = %+v, want the oversize file counted as skipped", report)
	}
	if got := hitPathsOfKind(report.Hits, HitKindCode); !contains(got, "hit.txt") || contains(got, "big.txt") {
		t.Fatalf("hits = %v", got)
	}
}

func TestCodeScanFindsFileThatShrankBelowSizeLimit(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "shrunk.txt")
	testutil.FailErr(t, "write current content", os.WriteFile(abs, []byte("needle\n"), 0o600))
	matcher, err := compileCodeQuery(TextExpr{Text: "needle"}, MatchFlags{})
	testutil.FailErr(t, "compile query", err)
	files := codeFilesFromEntries(CodeRoot{}, "root", filepath.Dir(abs), []sourcecatalog.Entry{
		{RootID: "root", Path: "shrunk.txt", Size: codeExecutorMaxFileBytes + 1},
	})
	out := scanOneCodeFile(t.Context(), codeScanJob{
		file: files[0], content: true,
	}, 0, codeScanSpec{matcher: matcher, wantLines: true, lineCap: 10, maxBytes: codeExecutorMaxFileBytes})
	if out.skipped || len(out.lineHits) != 1 {
		t.Fatalf("scan with stale size = %+v", out)
	}
}

func TestFileHitScoreRanksExactBasenameHighest(t *testing.T) {
	exact := fileHitScore("internal/search/toolbar.go", []string{"toolbar.go"})
	stem := fileHitScore("internal/search/toolbar.go", []string{"toolbar"})
	partial := fileHitScore("internal/search/toolbar.go", []string{"tool"})
	dirOnly := fileHitScore("internal/search/plan.go", []string{"search"})

	if exact != fileScoreExactName || stem != fileScoreExactName {
		t.Fatalf("exact basename/stem not top ranked: exact=%v stem=%v", exact, stem)
	}
	if partial >= exact {
		t.Fatalf("partial basename %v outranks exact %v", partial, exact)
	}
	if dirOnly >= partial {
		t.Fatalf("directory-only %v outranks basename %v", dirOnly, partial)
	}
	// Exact basename outranks the code-hit score cap.
	if exact <= codeScoreCap {
		t.Fatalf("exact file score %v does not lead code hits (cap %v)", exact, codeScoreCap)
	}
}

func TestUnderDependencyDirMatchesSegmentsAndPrefixes(t *testing.T) {
	set := dependencyDirSet([]string{"node_modules", ".yarn/cache", "bin/Debug", ""})
	cases := map[string]bool{
		"node_modules/pkg/index.js":     true,
		"web/node_modules/pkg/index.js": true,
		".yarn/cache/thing.zip":         true,
		"app/.yarn/cache/thing.zip":     true,
		"proj/bin/Debug/app.dll":        true,
		"src/node_modules_helper.ts":    false,
		"src/bin/main.go":               false,
		"docs/toolbar.md":               false,
	}
	for rel, want := range cases {
		if got := underDependencyDir(rel, set); got != want {
			t.Fatalf("underDependencyDir(%q) = %v, want %v", rel, got, want)
		}
	}
	if underDependencyDir("node_modules/x", dependencyDirSet(nil)) {
		t.Fatalf("empty catalog must exclude nothing")
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func TestFilterOnlyLineHitsSkipBlankAndPhantomLines(t *testing.T) {
	root := t.TempDir()
	// Trailing newlines and blank lines contribute no filter-only hits.
	body := "alpha\n\nbeta\n"
	testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(root, "doc.txt"), []byte(body), 0o644))
	hits := runCodeLeg(t, &CodePlanLeg{
		Query:     FilterExpr{Field: "kind", Value: HitKindCode},
		PathRoots: []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
		Lines:     true,
		Cap:       SearchExecutorProbeHits,
	})
	if len(hits) != 2 {
		t.Fatalf("hits = %+v, want the two content lines", hits)
	}
	if hits[0].Line != 1 || hits[1].Line != 3 {
		t.Fatalf("lines = %d, %d, want 1 and 3", hits[0].Line, hits[1].Line)
	}
}

func TestLongLineSnippetWindowsAroundMatch(t *testing.T) {
	root := t.TempDir()
	line := strings.Repeat("x", 5000) + " window_needle " + strings.Repeat("y", 5000)
	testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(root, "long.txt"), []byte(line+"\n"), 0o644))
	hits := runCodeLeg(t, &CodePlanLeg{
		Query:     TextExpr{Text: "window_needle"},
		PathRoots: []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
		Lines:     true,
		Cap:       SearchExecutorProbeHits,
	})
	if len(hits) != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	snippet := hits[0].Snippet
	if len(snippet) > codeSnippetMaxBytes+2*len("…") {
		t.Fatalf("snippet length = %d, want ≤ %d", len(snippet), codeSnippetMaxBytes)
	}
	if !strings.Contains(snippet, "window_needle") {
		t.Fatalf("snippet lost the match: %q", snippet)
	}
	if !strings.HasPrefix(snippet, "…") || !strings.HasSuffix(snippet, "…") {
		t.Fatalf("snippet should mark both cut ends: %q", snippet)
	}
}

func TestUnreadableFileCountsAsSkipped(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write open file", os.WriteFile(filepath.Join(root, "open.txt"), []byte("needle\n"), 0o644))
	locked := filepath.Join(root, "locked.txt")
	testutil.FailErr(t, "write locked file", os.WriteFile(locked, []byte("needle\n"), 0o644))
	testutil.FailErr(t, "chmod locked file", os.Chmod(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o644) })
	report, err := NewCodeExecutor(decide.Reranker{}).Run(context.Background(), PlanLeg{Code: &CodePlanLeg{
		Query:     TextExpr{Text: "needle"},
		PathRoots: []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
		Lines:     true,
		Cap:       SearchExecutorProbeHits,
	}})
	testutil.FailErr(t, "run code leg", err)
	if len(report.Hits) != 1 {
		t.Fatalf("hits = %+v", report.Hits)
	}
	if report.SkippedFiles != 1 {
		t.Fatalf("skipped = %d, want 1", report.SkippedFiles)
	}
}

func TestLineScanHonorsDependencyExcludes(t *testing.T) {
	root := codeFixtureRoot(t)
	testutil.FailErr(t, "write vendored body", os.WriteFile(
		filepath.Join(root, "node_modules", "toolbar", "body.js"), []byte("toolbar body\n"), 0o644))
	hits := runCodeLeg(t, &CodePlanLeg{
		Query:           TextExpr{Text: "toolbar"},
		PathRoots:       []CodeRoot{{ProjectID: "p1", RootID: "root", Path: root}},
		Lines:           true,
		Cap:             SearchExecutorProbeHits,
		LineExcludeDirs: []string{"node_modules", "dist"},
	})
	for _, hit := range hits {
		if strings.HasPrefix(hit.Path, "node_modules/") || strings.HasPrefix(hit.Path, "dist/") {
			t.Fatalf("dependency tree leaked into line hits: %+v", hit)
		}
	}
	if len(hitPathsOfKind(hits, HitKindCode)) == 0 {
		t.Fatal("expected first-party line hits")
	}
}

func TestHumanSearchIncludesAgentExcludedPathsAndTargetedOverlay(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	root := t.TempDir()
	for rel, body := range map[string]string{
		".paintedwolf/source-scope.yaml": "version: 1\nexclude: [private/]\n",
		".paintedwolf/notes.txt":         "scope_needle",
		"private/notes.txt":              "scope_needle",
	} {
		target := filepath.Join(root, rel)
		testutil.FailErr(t, "create search fixture directory", os.MkdirAll(filepath.Dir(target), 0700))
		testutil.FailErr(t, "write search fixture", os.WriteFile(target, []byte(body), 0600))
	}
	provider, err := sourcescope.NewProvider(sourcescope.Config{}, func(context.Context, string) bool { return true })
	testutil.FailErr(t, "source policy", err)
	if provider.Capture(t.Context(), root).AdmitPath("private/notes.txt", false) {
		t.Fatal("fixture must be excluded from agent capture")
	}
	catalog := sourcecatalog.New()
	catalog.SetScopes(provider)
	t.Cleanup(func() { testutil.FailErr(t, "drain search catalog", catalog.Drain(context.Background())) })
	executor := &CodeExecutor{catalog: catalog}
	roots := []CodeRoot{{ProjectID: "p", RootID: "r", Path: root}}
	for _, tc := range []struct {
		query Node
		want  string
	}{
		{TextExpr{Text: "scope_needle"}, "private/notes.txt"},
		{AndExpr{Exprs: []Node{TextExpr{Text: "scope_needle"}, FilterExpr{Field: "path", Value: ".paintedwolf"}}}, ".paintedwolf/notes.txt"},
	} {
		report, err := executor.Run(t.Context(), PlanLeg{Code: &CodePlanLeg{Query: tc.query, PathRoots: roots, Lines: true, Cap: 10}})
		testutil.FailErr(t, "search human source scope", err)
		if len(report.Hits) != 1 || report.Hits[0].Path != tc.want {
			t.Fatalf("hits=%+v want %s", report.Hits, tc.want)
		}
	}
}
