package survey

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

func TestGrepEngine_PrefilterSkipsNonMatchingFiles(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 40; i++ {
		name := filepath.Join(dir, "noise_"+strconv.Itoa(i)+".txt")
		testutil.FailErr(t, "write noise", os.WriteFile(name, []byte("lorem ipsum dolor sit amet\n"), 0o644))
	}
	testutil.FailErr(t, "write hit", os.WriteFile(filepath.Join(dir, "hit.txt"), []byte("unique_token_xyz\n"), 0o644))

	stats := &grepEngineStats{}
	re, err := compileGrepRegex("unique_token_xyz", false)
	testutil.FailErr(t, "compile", err)
	search := &grepSearch{
		ctx:        context.Background(),
		reads:      projectpaths.NewReadSession(nativefixture.Boundary(t), nativefixture.Context(dir)),
		pattern:    "unique_token_xyz",
		re:         re,
		require:    litprefilter.Extract("unique_token_xyz", false),
		stats:      stats,
		resp:       &grepResponse{Matches: []grepMatch{}},
		maxMatches: safecmd.GrepMaxMatches,
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	_, targets, err := tool.resolveGrepTargets(context.Background(), nativefixture.Context(dir), map[string]any{"path": "."})
	testutil.FailErr(t, "targets", err)
	for _, target := range targets {
		testutil.FailErr(t, "walk", tool.grepWalkTreeParallel(context.Background(), nativefixture.Context(dir), target, false, search, sandbox.SurveyOptions{}))
	}
	if len(search.resp.Matches) != 1 {
		t.Fatalf("matches=%d", len(search.resp.Matches))
	}
	if stats.PrefilterSkipped.Load() < 30 {
		t.Fatalf("prefilter skipped=%d want ≥30", stats.PrefilterSkipped.Load())
	}
	if stats.RegexOrScanRuns.Load() != 1 {
		t.Fatalf("regex/scan runs=%d want 1", stats.RegexOrScanRuns.Load())
	}
}

func TestGrepEngine_BinarySkipCounted(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write text", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("needle\n"), 0o644))
	bin := append([]byte("nope"), 0)
	bin = append(bin, []byte("needle")...)
	testutil.FailErr(t, "write bin", os.WriteFile(filepath.Join(dir, "b.bin"), bin, 0o644))

	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern": "needle",
	}, nativefixture.Context(dir))
	testutil.FailErr(t, "grep", err)
	matches := nativefixture.GrepMatches(t, out)
	if len(matches) != 1 {
		t.Fatalf("matches=%+v", matches)
	}
	var wrap struct {
		BinarySkipped int `json:"binary_skipped"`
	}
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(nativefixture.SurveyContent(t, out)), &wrap))
	if wrap.BinarySkipped < 1 {
		t.Fatalf("binary_skipped=%d", wrap.BinarySkipped)
	}
}

func TestGrepEngine_WorkerCapBounded(t *testing.T) {
	w := grepWorkerCount()
	if w > grepWorkerMax {
		t.Fatalf("workers=%d > cap %d", w, grepWorkerMax)
	}
	procs := runtime.GOMAXPROCS(0)
	if procs > 4 && w >= procs {
		t.Fatalf("workers=%d not ≪ GOMAXPROCS=%d", w, procs)
	}
}

func TestGrepEngine_MatchCapAbortsMidFlight(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 30; i++ {
		testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "f_"+strconv.Itoa(i)+".txt"), []byte("hit line\n"), 0o644))
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern":     "hit",
		"max_matches": 5,
	}, nativefixture.Context(dir))
	testutil.FailErr(t, "grep", err)
	matches := nativefixture.GrepMatches(t, out)
	if len(matches) != 5 {
		t.Fatalf("len=%d", len(matches))
	}
	var wrap struct {
		Truncated  bool `json:"truncated"`
		NextOffset *int `json:"next_offset"`
	}
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(nativefixture.SurveyContent(t, out)), &wrap))
	if !wrap.Truncated || wrap.NextOffset == nil || *wrap.NextOffset != 5 {
		t.Fatalf("pagination=%+v", wrap)
	}
}

func TestGrepEngine_BloomFilterPrunesNonMatchingFiles(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 25; i++ {
		name := filepath.Join(dir, "noise_"+strconv.Itoa(i)+".txt")
		testutil.FailErr(t, "write noise", os.WriteFile(name, []byte("lorem ipsum dolor sit amet consectetur adipiscing elit\n"), 0o644))
	}
	testutil.FailErr(t, "write hit", os.WriteFile(filepath.Join(dir, "hit.txt"), []byte("package main\nfunc TargetNeedleToken() {}\n"), 0o644))

	catalog := sourcecatalog.New()
	snapshot, err := catalog.Observe(context.Background(), "test-proj", []sourcecatalog.Root{{ID: "r1", Path: dir}})
	testutil.FailErr(t, "observe root", err)

	// Populate Bloom filters.
	opener := func(entry sourcecatalog.Entry) (io.ReadCloser, error) {
		return os.Open(filepath.Join(dir, filepath.FromSlash(entry.Path)))
	}
	_, err = catalog.Literals.LiteralCandidates(context.Background(), snapshot, sourcecatalog.LiteralQuery{
		RootID: "r1", Base: ".", Require: litprefilter.AnyOf("TargetNeedleToken"), Open: opener,
	})
	testutil.FailErr(t, "populate blooms", err)

	stats := &grepEngineStats{}
	re, err := compileGrepRegex("TargetNeedleToken", false)
	testutil.FailErr(t, "compile", err)
	search := &grepSearch{
		ctx:        context.Background(),
		reads:      projectpaths.NewReadSession(nativefixture.Boundary(t), nativefixture.Context(dir)),
		pattern:    "TargetNeedleToken",
		re:         re,
		require:    litprefilter.Extract("TargetNeedleToken", false),
		stats:      stats,
		resp:       &grepResponse{Matches: []grepMatch{}},
		maxMatches: safecmd.GrepMaxMatches,
	}

	tool := &GrepTool{Boundary: nativefixture.Boundary(t), Catalog: catalog}
	_, targets, err := tool.resolveGrepTargets(context.Background(), nativefixture.Context(dir), map[string]any{"path": "."})
	testutil.FailErr(t, "targets", err)
	for _, target := range targets {
		testutil.FailErr(t, "walk", tool.grepWalkTreeParallel(context.Background(), nativefixture.Context(dir), target, false, search, sandbox.SurveyOptions{}))
	}

	if len(search.resp.Matches) != 1 {
		t.Fatalf("matches=%d want 1", len(search.resp.Matches))
	}
	if pruned := stats.IndexBloomPruned.Load(); pruned < 20 {
		t.Fatalf("pruned by bloom=%d want >= 20", pruned)
	}
	if opened := stats.FilesOpened.Load(); opened > 5 {
		t.Fatalf("files opened=%d want <= 5", opened)
	}
}

func TestGrepEngine_UncachedFileScannedDirectly(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write old", os.WriteFile(filepath.Join(dir, "old.txt"), []byte("stable text\n"), 0o644))

	catalog := sourcecatalog.New()
	snapshot, err := catalog.Observe(context.Background(), "test-proj", []sourcecatalog.Root{{ID: "r1", Path: dir}})
	testutil.FailErr(t, "observe root", err)

	opener := func(entry sourcecatalog.Entry) (io.ReadCloser, error) {
		return os.Open(filepath.Join(dir, filepath.FromSlash(entry.Path)))
	}
	_, err = catalog.Literals.LiteralCandidates(context.Background(), snapshot, sourcecatalog.LiteralQuery{
		RootID: "r1", Base: ".", Require: litprefilter.AnyOf("fresh_token"), Open: opener,
	})
	testutil.FailErr(t, "populate blooms", err)

	// Write unindexed file.
	testutil.FailErr(t, "write new", os.WriteFile(filepath.Join(dir, "new.txt"), []byte("contains fresh_token here\n"), 0o644))
	catalog.InvalidateRoot(dir)

	tool := &GrepTool{Boundary: nativefixture.Boundary(t), Catalog: catalog}
	out, err := tool.Run(context.Background(), map[string]any{"pattern": "fresh_token"}, nativefixture.Context(dir))
	testutil.FailErr(t, "run grep", err)
	matches := nativefixture.GrepMatches(t, out)
	if len(matches) != 1 || matches[0]["path"] != "new.txt" {
		t.Fatalf("matches=%+v want match in new.txt", matches)
	}
}

// An alternation prunes through the index too: a file whose bloom excludes
// every branch is never opened.
func TestGrepEngine_BloomFilterPrunesAlternations(t *testing.T) {
	dir := t.TempDir()
	for i := range 25 {
		name := filepath.Join(dir, "noise_"+strconv.Itoa(i)+".txt")
		testutil.FailErr(t, "write noise", os.WriteFile(name, []byte("lorem ipsum dolor sit amet consectetur adipiscing elit\n"), 0o644))
	}
	testutil.FailErr(t, "write first hit", os.WriteFile(filepath.Join(dir, "a_hit.txt"), []byte("projection_marker := 1\n"), 0o644))
	testutil.FailErr(t, "write second hit", os.WriteFile(filepath.Join(dir, "b_hit.txt"), []byte("type ProjectionMarker struct{}\n"), 0o644))

	catalog := sourcecatalog.New()
	snapshot, err := catalog.Observe(context.Background(), "test-proj", []sourcecatalog.Root{{ID: "r1", Path: dir}})
	testutil.FailErr(t, "observe root", err)
	opener := func(entry sourcecatalog.Entry) (io.ReadCloser, error) {
		return os.Open(filepath.Join(dir, filepath.FromSlash(entry.Path)))
	}
	_, err = catalog.Literals.LiteralCandidates(context.Background(), snapshot, sourcecatalog.LiteralQuery{
		RootID: "r1", Base: ".", Require: litprefilter.AnyOf("projection_marker"), Open: opener,
	})
	testutil.FailErr(t, "populate blooms", err)

	const pattern = "projection_marker|ProjectionMarker"
	stats := &grepEngineStats{}
	re, err := compileGrepRegex(pattern, false)
	testutil.FailErr(t, "compile", err)
	search := &grepSearch{
		ctx: context.Background(), reads: projectpaths.NewReadSession(nativefixture.Boundary(t), nativefixture.Context(dir)),
		pattern: pattern, re: re, require: litprefilter.Extract(pattern, false), stats: stats,
		resp: &grepResponse{Matches: []grepMatch{}}, maxMatches: safecmd.GrepMaxMatches,
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t), Catalog: catalog}
	_, targets, err := tool.resolveGrepTargets(context.Background(), nativefixture.Context(dir), map[string]any{"path": "."})
	testutil.FailErr(t, "targets", err)
	for _, target := range targets {
		testutil.FailErr(t, "walk", tool.grepWalkTreeParallel(context.Background(), nativefixture.Context(dir), target, false, search, sandbox.SurveyOptions{}))
	}
	if len(search.resp.Matches) != 2 {
		t.Fatalf("matches=%d want 2", len(search.resp.Matches))
	}
	if pruned := stats.IndexBloomPruned.Load(); pruned < 20 {
		t.Fatalf("pruned by bloom=%d want >= 20", pruned)
	}
	if opened := stats.FilesOpened.Load(); opened > 7 {
		t.Fatalf("files opened=%d want <= 7", opened)
	}
}

// Files finish on many workers, but outcomes apply in walk order, so a capped
// search returns the same page every time.
func TestGrepEngine_StreamAppliesOutcomesInWalkOrder(t *testing.T) {
	dir := t.TempDir()
	for i := range 300 {
		name := filepath.Join(dir, fmt.Sprintf("f%03d.txt", i))
		testutil.FailErr(t, "write fixture", os.WriteFile(name, []byte(strings.Repeat("filler line\n", i%17)+"needle\n"), 0o644))
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	var first []string
	for run := range 3 {
		out, err := tool.Run(context.Background(), map[string]any{"pattern": "needle", "max_matches": 40}, nativefixture.Context(dir))
		testutil.FailErr(t, "run grep", err)
		var paths []string
		for _, match := range nativefixture.GrepMatches(t, out) {
			paths = append(paths, match["path"].(string))
		}
		if len(paths) != 40 || !slices.IsSorted(paths) {
			t.Fatalf("run %d returned %d matches in order %v", run, len(paths), paths)
		}
		if run == 0 {
			first = paths
		} else if !slices.Equal(paths, first) {
			t.Fatalf("run %d page differs:\n%v\n%v", run, paths, first)
		}
	}
}
