package localdata

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestKnownCatalog(t *testing.T) {
	t.Parallel()
	if len(Catalog()) != 13 {
		t.Fatalf("catalog len=%d want 13", len(Catalog()))
	}
	for _, id := range Catalog() {
		if !Known(id) {
			t.Fatalf("Known(%q)=false", id)
		}
	}
	if Known("store_db") || Known("") {
		t.Fatal("unknown ids must not be Known")
	}
}

func TestClearIdempotentEmpty(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	reg, err := New(base)
	testutil.FailErr(t, "New", err)
	reg.SetWebIndexClear(func(context.Context) error { return nil })

	for _, id := range Catalog() {
		if err := reg.ClearOne(context.Background(), id); err != nil {
			t.Fatalf("clear empty %s: %v", id, err)
		}
		if err := reg.ClearOne(context.Background(), id); err != nil {
			t.Fatalf("second clear %s: %v", id, err)
		}
	}
}

func TestClearBucketsUnknownRejects(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	reg, err := New(base)
	testutil.FailErr(t, "New", err)

	_, err = reg.ClearBuckets(context.Background(), []string{BucketFetchCache, "not_a_bucket"})
	if err == nil {
		t.Fatal("expected unknown bucket error")
	}
	if err := os.WriteFile(filepath.Join(enginepaths.FetchCacheRootUnder(base), "x"), []byte("x"), 0o600); err != nil {
		_ = os.MkdirAll(enginepaths.FetchCacheRootUnder(base), 0o700)
		testutil.FailErr(t, "seed fetch", os.WriteFile(filepath.Join(enginepaths.FetchCacheRootUnder(base), "x"), []byte("x"), 0o600))
	}
	_, err = reg.ClearBuckets(context.Background(), []string{"bogus"})
	if err == nil {
		t.Fatal("expected unknown")
	}
	if !pathExists(filepath.Join(enginepaths.FetchCacheRootUnder(base), "x")) {
		t.Fatal("reject must not clear other buckets")
	}
}

func TestClearBucketsEmptyRejects(t *testing.T) {
	t.Parallel()
	reg, err := New(t.TempDir())
	testutil.FailErr(t, "New", err)
	_, err = reg.ClearBuckets(context.Background(), nil)
	if err == nil {
		t.Fatal("empty buckets must error")
	}
}

func TestClearDoesNotTouchDurable(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	testutil.FailErr(t, "seed durable", SeedDurableMarkers(base))

	testutil.FailErr(t, "mkdir fetch", os.MkdirAll(enginepaths.FetchCacheRootUnder(base), 0o700))
	testutil.FailErr(t, "write fetch", os.WriteFile(filepath.Join(enginepaths.FetchCacheRootUnder(base), "a"), []byte("a"), 0o600))
	testutil.FailErr(t, "mkdir osv", os.MkdirAll(project.OSVCacheDir(base), 0o750))
	testutil.FailErr(t, "write osv", os.WriteFile(filepath.Join(project.OSVCacheDir(base), "db"), []byte("o"), 0o600))
	testutil.FailErr(t, "mkdir model", os.MkdirAll(enginepaths.ModelfeedRootUnder(base), 0o700))
	testutil.FailErr(t, "write model", os.WriteFile(filepath.Join(enginepaths.ModelfeedRootUnder(base), "api.json"), []byte("{}"), 0o600))
	testutil.FailErr(t, "mkdir browser", os.MkdirAll(enginepaths.BrowserCacheRootUnder(base), 0o700))
	testutil.FailErr(t, "write browser", os.WriteFile(filepath.Join(enginepaths.BrowserCacheRootUnder(base), "bin"), []byte("b"), 0o600))
	testutil.FailErr(t, "mkdir debug tree", os.MkdirAll(filepath.Join(debugpaths.DebugRootUnder(base), "sessions", "t"), 0o700))
	testutil.FailErr(t, "write debug", os.WriteFile(filepath.Join(debugpaths.DebugRootUnder(base), "sessions", "t", "llm-requests.jsonl"), []byte("{}\n"), 0o600))
	testutil.FailErr(t, "mkdir pricing", os.MkdirAll(enginepaths.PricingCacheRootUnder(base), 0o700))
	testutil.FailErr(t, "write pricing", os.WriteFile(filepath.Join(enginepaths.PricingCacheRootUnder(base), "rates.json"), []byte("{}"), 0o600))
	testutil.FailErr(t, "mkdir extensions", os.MkdirAll(enginepaths.ExtensionsCacheRootUnder(base), 0o700))
	testutil.FailErr(t, "write extensions", os.WriteFile(filepath.Join(enginepaths.ExtensionsCacheRootUnder(base), "body"), []byte("p"), 0o600))
	testutil.FailErr(t, "mkdir extensions-meta", os.MkdirAll(enginepaths.ExtensionsMetaRootUnder(base), 0o700))
	testutil.FailErr(t, "write extensions-meta", os.WriteFile(filepath.Join(enginepaths.ExtensionsMetaRootUnder(base), "meta.yaml"), []byte("id: x\n"), 0o600))
	testutil.FailErr(t, "write verify-detect", os.WriteFile(enginepaths.VerifyDetectPathUnder(base), []byte("{}"), 0o600))
	testutil.FailErr(t, "write web-index", os.WriteFile(webIndexPath(base), []byte("sqlite"), 0o600))
	testutil.FailErr(t, "write source observations", os.WriteFile(sourceObservationsPath(base), []byte("sqlite"), 0o600))
	for _, path := range sourceObservationPaths(base)[1:] {
		testutil.FailErr(t, "write source observations sidecar", os.WriteFile(path, []byte("journal"), 0o600))
	}
	proj := filepath.Join(base, "projects", "p1", "tool-output")
	testutil.FailErr(t, "mkdir project", os.MkdirAll(proj, 0o700))
	testutil.FailErr(t, "write spill", os.WriteFile(filepath.Join(proj, "out"), []byte("x"), 0o600))
	draft := filepath.Join(base, "drafts", "d1")
	testutil.FailErr(t, "mkdir draft", os.MkdirAll(draft, 0o700))
	testutil.FailErr(t, "write draft", os.WriteFile(filepath.Join(draft, "f"), []byte("y"), 0o600))
	seed := filepath.Join(enginepaths.WorkerSeedsRootUnder(base), "0123456789abcdef", "generation")
	testutil.FailErr(t, "mkdir worker seed", os.MkdirAll(seed, 0o700))
	testutil.FailErr(t, "write worker seed", os.WriteFile(filepath.Join(seed, "f"), []byte("z"), 0o600))
	catalog := filepath.Join(enginepaths.SourceCatalogCacheRootUnder(base), "tree-v1")
	testutil.FailErr(t, "mkdir source catalog", os.MkdirAll(catalog, 0o700))
	testutil.FailErr(t, "write source catalog", os.WriteFile(filepath.Join(catalog, "generation.db"), []byte("sqlite"), 0o600))

	reg, err := New(base)
	testutil.FailErr(t, "New", err)
	// Without a callback, web_index removes the file itself.
	reg.SetWebIndexClear(nil)

	results, err := reg.ClearBuckets(context.Background(), Catalog())
	testutil.FailErr(t, "ClearBuckets", err)
	for _, res := range results {
		if !res.OK {
			t.Fatalf("clear %s failed: %s", res.ID, res.Error)
		}
	}

	for _, p := range DurableAbsPaths(base) {
		data, err := os.ReadFile(p)
		testutil.FailErr(t, "read durable "+p, err)
		if string(data) != "keep\n" {
			t.Fatalf("durable mutated: %s", p)
		}
	}

	if dirNonEmpty(enginepaths.FetchCacheRootUnder(base)) {
		t.Fatal("fetch_cache still non-empty")
	}
	if dirNonEmpty(enginepaths.PricingCacheRootUnder(base)) {
		t.Fatal("pricing_cache still non-empty")
	}
	if dirNonEmpty(enginepaths.ExtensionsCacheRootUnder(base)) || dirNonEmpty(enginepaths.ExtensionsMetaRootUnder(base)) {
		t.Fatal("extension_cache still non-empty")
	}
	if pathExists(enginepaths.VerifyDetectPathUnder(base)) {
		t.Fatal("scan_scratch remained")
	}
	if dirNonEmpty(enginepaths.SourceCatalogCacheRootUnder(base)) {
		t.Fatal("source_catalog still non-empty")
	}
	if pathExists(webIndexPath(base)) {
		t.Fatal("web_index file should be removed when no Store clear")
	}
	for _, path := range sourceObservationPaths(base) {
		if pathExists(path) {
			t.Fatalf("source_observations path should be removed when no Store clear: %s", path)
		}
	}
	if pathExists(debugpaths.DebugRootUnder(base)) {
		t.Fatal("debug/ tree remained")
	}
	if !pathExists(filepath.Join(proj, "out")) || !pathExists(filepath.Join(draft, "f")) {
		t.Fatal("cache clear removed retained project content or a draft workspace")
	}
	if !pathExists(filepath.Join(seed, "f")) {
		t.Fatal("bucket clear removed independently managed worker cache")
	}
}

func TestWebIndexClearUsesCallback(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	reg, err := New(base)
	testutil.FailErr(t, "New", err)
	called := 0
	reg.SetWebIndexClear(func(context.Context) error {
		called++
		return nil
	})
	testutil.FailErr(t, "seed", os.WriteFile(webIndexPath(base), []byte("x"), 0o600))
	testutil.FailErr(t, "clear", reg.ClearOne(context.Background(), BucketWebIndex))
	if called != 1 {
		t.Fatalf("clear callback calls=%d", called)
	}
	// The callback owns clearing; the open index file stays.
	if !pathExists(webIndexPath(base)) {
		t.Fatal("Store.Clear path must not also RemoveAll the db file")
	}
}

func TestSourceObservationsClearUsesCallback(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	reg, err := New(base)
	testutil.FailErr(t, "New", err)
	called := 0
	reg.SetSourceObservationsClear(func(context.Context) error {
		called++
		return nil
	})
	testutil.FailErr(t, "seed", os.WriteFile(sourceObservationsPath(base), []byte("x"), 0o600))
	testutil.FailErr(t, "seed orientation directory", os.MkdirAll(enginepaths.RepoOrientationRootUnder(base), 0o700))
	testutil.FailErr(t, "seed orientation", os.WriteFile(filepath.Join(enginepaths.RepoOrientationRootUnder(base), "brief.json"), []byte("{}"), 0o600))
	testutil.FailErr(t, "clear", reg.ClearOne(context.Background(), BucketSourceObservations))
	if called != 1 {
		t.Fatalf("clear callback calls=%d", called)
	}
	if !pathExists(sourceObservationsPath(base)) {
		t.Fatal("open cache clear must not also remove the database file")
	}
	if pathExists(enginepaths.RepoOrientationRootUnder(base)) {
		t.Fatal("orientation cache survived observation clear")
	}
}

func TestSourceCatalogClearRunsStorageRemovalInsideCallback(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	reg, err := New(base)
	testutil.FailErr(t, "New", err)
	dir := enginepaths.SourceCatalogCacheRootUnder(base)
	testutil.FailErr(t, "create catalog cache", os.MkdirAll(dir, 0o700))
	testutil.FailErr(t, "seed catalog cache", os.WriteFile(filepath.Join(dir, "page.tree"), []byte("x"), 0o600))
	called := false
	reg.SetSourceCatalogClear(func(_ context.Context, clear func() error) error {
		called = true
		if !dirNonEmpty(dir) {
			t.Fatal("catalog storage cleared before lifecycle callback")
		}
		return clear()
	})

	testutil.FailErr(t, "clear source catalog", reg.ClearOne(t.Context(), BucketSourceCatalog))
	if !called {
		t.Fatal("source catalog lifecycle callback was not called")
	}
	if dirNonEmpty(dir) {
		t.Fatal("source catalog storage survived clear callback")
	}
}

func TestSourceCatalogClearEmptiesTheCacheTree(t *testing.T) {
	base := t.TempDir()
	reg, err := New(base)
	testutil.FailErr(t, "New", err)
	dir := filepath.Join(enginepaths.SourceCatalogCacheRootUnder(base), "tree-v1")
	testutil.FailErr(t, "create catalog cache", os.MkdirAll(dir, 0o700))
	stale := filepath.Join(dir, "obsolete.tree")
	testutil.FailErr(t, "seed obsolete cache", os.WriteFile(stale, []byte("stale"), 0o600))
	reg.SetSourceCatalogClear(func(_ context.Context, clear func() error) error {
		return clear()
	})

	// Spilled segments are nameless, so clearing the tree cannot reach a live
	// one and the callback has nothing to spare.
	testutil.FailErr(t, "clear source catalog", reg.ClearOne(t.Context(), BucketSourceCatalog))
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("obsolete cache remains: %v", err)
	}
	status, err := reg.Status(t.Context(), BucketSourceCatalog)
	testutil.FailErr(t, "source catalog status", err)
	if status.Bytes != 0 {
		t.Fatalf("source catalog bytes after clear = %d", status.Bytes)
	}
}

func TestClearPartialReporting(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	reg, err := New(base)
	testutil.FailErr(t, "New", err)
	reg.SetWebIndexClear(func(context.Context) error {
		return context.Canceled
	})
	results, err := reg.ClearBuckets(context.Background(), []string{BucketWebIndex, BucketFetchCache})
	testutil.FailErr(t, "ClearBuckets", err)
	if len(results) != 2 {
		t.Fatalf("results=%d", len(results))
	}
	if results[0].OK || results[0].Error == "" {
		t.Fatalf("want web_index failure: %+v", results[0])
	}
	if !results[1].OK {
		t.Fatalf("fetch_cache should succeed: %+v", results[1])
	}
}

func TestSymlinkEscapeRefused(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	outside := t.TempDir()
	marker := filepath.Join(outside, "secret")
	testutil.FailErr(t, "outside", os.WriteFile(marker, []byte("secret"), 0o600))
	link := enginepaths.FetchCacheRootUnder(base)
	testutil.FailErr(t, "symlink", os.Symlink(outside, link))

	reg, err := New(base)
	testutil.FailErr(t, "New", err)
	err = reg.ClearOne(context.Background(), BucketFetchCache)
	if err == nil {
		t.Fatal("expected symlink escape error")
	}
	data, err := os.ReadFile(marker)
	testutil.FailErr(t, "read outside", err)
	if string(data) != "secret" {
		t.Fatal("outside target was mutated")
	}
}

func TestStatusPresentBytes(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	reg, err := New(base)
	testutil.FailErr(t, "New", err)
	st, err := reg.Status(t.Context(), BucketFetchCache)
	testutil.FailErr(t, "status empty", err)
	if st.Present || st.Bytes != 0 {
		t.Fatalf("empty status: %+v", st)
	}
	testutil.FailErr(t, "mkdir", os.MkdirAll(enginepaths.FetchCacheRootUnder(base), 0o700))
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(enginepaths.FetchCacheRootUnder(base), "b"), []byte("hello"), 0o600))
	st, err = reg.Status(t.Context(), BucketFetchCache)
	testutil.FailErr(t, "status", err)
	if !st.Present || st.Bytes < 5 {
		t.Fatalf("want present with bytes: %+v", st)
	}
}

func TestSessionScratchStatusAndClear(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	reg, err := New(base)
	testutil.FailErr(t, "New", err)

	st, err := reg.Status(t.Context(), BucketSessionScratch)
	testutil.FailErr(t, "status empty", err)
	if st.Present || st.Bytes != 0 {
		t.Fatalf("empty status: %+v", st)
	}

	scratchDir := filepath.Join(enginepaths.ScratchRootUnder(base), "sess-1")
	testutil.FailErr(t, "mkdir", os.MkdirAll(scratchDir, 0o700))
	st, err = reg.Status(t.Context(), BucketSessionScratch)
	testutil.FailErr(t, "status of an empty folder", err)
	if st.Present {
		t.Fatalf("an empty session folder reported content: %+v", st)
	}
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(scratchDir, "scratch.txt"), []byte("temporary data"), 0o600))

	st, err = reg.Status(t.Context(), BucketSessionScratch)
	testutil.FailErr(t, "status", err)
	if !st.Present || st.Bytes < 10 {
		t.Fatalf("want present with bytes: %+v", st)
	}

	testutil.FailErr(t, "clear session scratch", reg.ClearOne(t.Context(), BucketSessionScratch))
	st, err = reg.Status(t.Context(), BucketSessionScratch)
	testutil.FailErr(t, "status after clear", err)
	if st.Present || st.Bytes != 0 {
		t.Fatalf("status after clear: %+v", st)
	}
}

func TestSessionScratchClearDefersToTheEngine(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	reg, err := New(base)
	testutil.FailErr(t, "New", err)
	kept := filepath.Join(enginepaths.ScratchRootUnder(base), "busy", "live.txt")
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(kept), 0o700))
	testutil.FailErr(t, "write", os.WriteFile(kept, []byte("x"), 0o600))

	reclaimed := false
	reg.SetSessionScratchReclaim(func(context.Context) error {
		reclaimed = true
		return nil
	})
	testutil.FailErr(t, "clear session scratch", reg.ClearOne(t.Context(), BucketSessionScratch))
	if !reclaimed {
		t.Fatal("clear bypassed the engine's reclaim")
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("registry removed scratch the engine kept: %v", err)
	}
}
