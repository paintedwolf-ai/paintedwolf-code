package sourcecatalog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
)

func indexFixture(t *testing.T) (*Catalog, Root) {
	t.Helper()
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	c := New()
	t.Cleanup(func() { testutil.FailErr(t, "drain index discovery", c.Drain(context.Background())) })
	return c, Root{ID: "r", Path: t.TempDir()}
}

// Test roots share one budget and traversal policy.
type testScopes struct{ plane sourcescope.Plane }

func (s testScopes) Catalog(_ context.Context, root string) *sourcescope.Scope {
	return sourcescope.New(root, sourcescope.Options{Plane: s.plane})
}

func writeIndexFile(t *testing.T, root, rel, body string) {
	t.Helper()
	abs := filepath.Join(root, rel)
	testutil.FailErr(t, "create parent", os.MkdirAll(filepath.Dir(abs), 0o755))
	testutil.FailErr(t, "write source", os.WriteFile(abs, []byte(body), 0o600))
}

// waitIndex waits for discovery to settle.
func waitIndex(t *testing.T, c *Catalog, root Root) *IndexReader {
	t.Helper()
	until := time.Now().Add(30 * time.Second)
	for time.Now().Before(until) {
		reader, status, err := c.OpenIndex(t.Context(), "p", root, 250*time.Millisecond)
		testutil.FailErr(t, "open file index", err)
		if status.State == StateFailed {
			t.Fatalf("index failed: %s", status.Error)
		}
		if reader != nil {
			if status.Complete && !status.Refreshing {
				t.Cleanup(func() { _ = reader.Close() })
				return reader
			}
			_ = reader.Close()
		}
	}
	t.Fatal("index discovery did not finish")
	return nil
}

func indexedPaths(t *testing.T, r *IndexReader, scope FileScope) []string {
	t.Helper()
	paths, err := r.FilePathsPage(t.Context(), scope, "", TreeFilePageLimit)
	testutil.FailErr(t, "list indexed files", err)
	return paths
}

func TestIndexHoldsHiddenAndIgnoredPathsButNotRepositoryMetadata(t *testing.T) {
	c, root := indexFixture(t)
	for _, name := range []string{".hidden/generated.txt", "build/generated.txt", "src/main.go", ".git/config"} {
		writeIndexFile(t, root.Path, name, "x")
	}
	writeIndexFile(t, root.Path, ".gitignore", "build/\n.hidden/\n")
	testutil.FailErr(t, "empty folder", os.Mkdir(filepath.Join(root.Path, "empty"), 0o755))
	reader := waitIndex(t, c, root)

	all := indexedPaths(t, reader, FileScope{Audience: HumanAudience, IncludeHidden: true})
	want := []string{".gitignore", ".hidden/generated.txt", "build/generated.txt", "src/main.go"}
	if !slices.Equal(all, want) {
		t.Fatalf("all files=%v want=%v", all, want)
	}
	// Ignored and hidden paths are indexed; projections filter them.
	visible := indexedPaths(t, reader, FileScope{Audience: HumanAudience})
	if !slices.Equal(visible, []string{"build/generated.txt", "src/main.go"}) {
		t.Fatalf("visible files=%v", visible)
	}
	files, err := reader.FileCount(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true})
	testutil.FailErr(t, "root totals", err)
	if files != 4 {
		t.Fatalf("file count=%d", files)
	}
}

// Manual advancement exposes index state between batches.
func indexWalkFixture(t *testing.T, c *Catalog, root Root) (*indexStore, *indexWalk) {
	t.Helper()
	s, err := c.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "index store", err)
	// The schema install writes, so it waits its turn behind the catalog's own writers.
	release, err := s.write(t.Context())
	testutil.FailErr(t, "writer gate", err)
	s.mu.Lock()
	err = loadStore(t.Context(), s)
	s.mu.Unlock()
	release()
	testutil.FailErr(t, "install schema", err)
	db, err := openTreeDB(t.Context(), s.file)
	testutil.FailErr(t, "index database", err)
	t.Cleanup(func() { _ = db.Close() })
	osRoot, err := os.OpenRoot(root.Path)
	testutil.FailErr(t, "open root", err)
	t.Cleanup(func() { _ = osRoot.Close() })
	w := &indexWalk{store: s, db: db, root: osRoot, budget: indexBudget{limits: s.policy.budgets}}
	testutil.FailErr(t, "seed frontier", w.prepare(t.Context(), true, nil))
	return s, w
}

// stepIndexDir visits one frontier directory and publishes what it wrote;
// discovery itself publishes on a cadence.
func stepIndexDir(t *testing.T, w *indexWalk) string {
	t.Helper()
	dir, err := w.nextDir(t.Context())
	testutil.FailErr(t, "next frontier directory", err)
	if dir == "" {
		testutil.FailErr(t, "publish drained frontier", w.publish(t.Context()))
		return ""
	}
	testutil.FailErr(t, "visit "+dir, w.visitDir(t.Context(), dir))
	testutil.FailErr(t, "publish "+dir, w.publish(t.Context()))
	return dir
}

func openIndexReader(t *testing.T, s *indexStore) (*IndexReader, error) {
	t.Helper()
	db, tx, status, err := s.readTx(t.Context(), TreeStatus{State: StateReady})
	if err != nil {
		return nil, err
	}
	r := &IndexReader{db: db, tx: tx, Status: status, store: s}
	t.Cleanup(func() { _ = r.Close() })
	return r, nil
}

func TestIndexAnswersFromAPartialGenerationBeforeDiscoveryEnds(t *testing.T) {
	c, root := indexFixture(t)
	for _, dir := range []string{"alpha", "bravo", "charlie"} {
		for i := range 3 {
			writeIndexFile(t, root.Path, fmt.Sprintf("%s/%02d.go", dir, i), "source")
		}
	}
	s, w := indexWalkFixture(t, c, root)

	// Seeding alone publishes nothing, so the store stays warming.
	if _, err := openIndexReader(t, s); err == nil {
		t.Fatal("seeding published a generation with no rows")
	}

	if dir := stepIndexDir(t, w); dir != "." {
		t.Fatalf("first frontier directory=%q", dir)
	}
	reader, err := openIndexReader(t, s)
	testutil.FailErr(t, "read partial generation", err)
	if reader.Status.Complete {
		t.Fatal("a generation covering one directory reported whole coverage")
	}
	coverage, err := reader.Coverage(t.Context())
	testutil.FailErr(t, "read partial coverage", err)
	if !coverage.Pending() || coverage.Exhaustive() {
		t.Fatalf("readable partial generation claims settled coverage: %+v", coverage)
	}
	// One listing in, the index already answers.
	first := indexedPaths(t, reader, FileScope{Audience: HumanAudience, IncludeHidden: true})
	if len(first) != 0 {
		t.Fatalf("root listing holds no files of its own: %v", first)
	}
	stepIndexDir(t, w)
	partial, err := openIndexReader(t, s)
	testutil.FailErr(t, "read second generation", err)
	if got := indexedPaths(t, partial, FileScope{Audience: HumanAudience, IncludeHidden: true}); len(got) != 3 {
		t.Fatalf("after one subdirectory: %v", got)
	}
	if partial.Status.Complete {
		t.Fatal("partial generation reported whole coverage")
	}

	for stepIndexDir(t, w) != "" {
	}
	testutil.FailErr(t, "finish discovery", w.finish(t.Context(), s.epoch))
	whole, err := openIndexReader(t, s)
	testutil.FailErr(t, "read complete generation", err)
	if !whole.Status.Complete || len(indexedPaths(t, whole, FileScope{Audience: HumanAudience, IncludeHidden: true})) != 9 {
		t.Fatalf("complete=%v files=%v", whole.Status.Complete, indexedPaths(t, whole, FileScope{Audience: HumanAudience, IncludeHidden: true}))
	}
}

// Index waiters wake on the first publication; summary waiters need complete coverage.
func TestJoinWakesOnFirstGenerationOnlyForPartialProjections(t *testing.T) {
	c, root := indexFixture(t)
	writeIndexFile(t, root.Path, "src/main.go", "source")
	index, err := c.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "index store", err)
	summary, err := c.summaryStore(t.Context(), "p", root, TreeScope{Key: "orientation"})
	testutil.FailErr(t, "summary store", err)
	if !index.partial() || summary.partial() {
		t.Fatalf("partial: index=%v summary=%v", index.partial(), summary.partial())
	}

	testutil.FailErr(t, "load index", loadStore(t.Context(), index))
	testutil.FailErr(t, "load summary", loadStore(t.Context(), summary))
	for _, s := range []projectionStore{index, summary} {
		core := s.core()
		core.mu.Lock()
		core.startRefresh(t.Context(), s, c.broker, core.epoch)
		armed := core.readable != nil
		core.mu.Unlock()
		if armed != s.partial() {
			t.Fatalf("%T armed=%v want %v", s, armed, s.partial())
		}
	}
	testutil.FailErr(t, "drain passes", c.Drain(context.Background()))
}

// A cold root answers within the join rather than after the whole walk.
func TestIndexJoinReturnsAGenerationOnAColdRoot(t *testing.T) {
	c, root := indexFixture(t)
	for i := range 400 {
		writeIndexFile(t, root.Path, fmt.Sprintf("dir%02d/file%02d.go", i/8, i%8), "source")
	}
	started := time.Now()
	reader, status, err := c.OpenIndex(t.Context(), "p", root, 20*time.Second)
	testutil.FailErr(t, "join cold index", err)
	if reader == nil {
		t.Fatalf("join returned no generation: %+v", status)
	}
	t.Cleanup(func() { _ = reader.Close() })
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("join took %s", elapsed)
	}
}

func TestIndexWalksDeferredDirectoriesAfterProjectSource(t *testing.T) {
	c, root := indexFixture(t)
	c.SetScopes(testScopes{plane: sourcescope.Plane{DeferIgnored: true}})
	// Alphabetical order puts artifacts before src.
	writeIndexFile(t, root.Path, ".gitignore", "artifacts/\n")
	writeIndexFile(t, root.Path, "artifacts/bundle.js", "generated")
	writeIndexFile(t, root.Path, "src/main.go", "source")

	_, w := indexWalkFixture(t, c, root)
	var order []string
	for {
		dir := stepIndexDir(t, w)
		if dir == "" {
			break
		}
		order = append(order, dir)
	}
	if !slices.Equal(order, []string{".", "src", "artifacts"}) {
		t.Fatalf("walk order=%v want the ignored tree last", order)
	}
}

func TestIndexBudgetRecordsTheBoundThatRefusedADirectory(t *testing.T) {
	for _, tc := range []struct {
		name    string
		plane   sourcescope.Plane
		wantDir string
		reason  sandbox.BoundaryReason
	}{
		{
			name:    "directory cap",
			plane:   sourcescope.Plane{Budgets: sandbox.SurveyBudgets{DirectoryEntries: 2}},
			wantDir: "wide",
			reason:  sandbox.BoundaryDirectoryCap,
		},
		{
			name:    "subtree cap",
			plane:   sourcescope.Plane{Budgets: sandbox.SurveyBudgets{SubtreeEntries: 2}},
			wantDir: "wide",
			reason:  sandbox.BoundarySubtreeCap,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, root := indexFixture(t)
			c.SetScopes(testScopes{plane: tc.plane})
			for i := range 6 {
				writeIndexFile(t, root.Path, fmt.Sprintf("wide/%02d.go", i), "source")
			}
			writeIndexFile(t, root.Path, "narrow/main.go", "source")
			reader := waitIndex(t, c, root)
			bounds := indexRefusals(t, c, root)
			if len(bounds) != 1 || bounds[0].path != tc.wantDir || bounds[0].reason != tc.reason {
				t.Fatalf("boundaries=%+v want %s/%s", bounds, tc.wantDir, tc.reason)
			}
			// The same fact reaches a search as a count it can report.
			coverage, err := reader.Coverage(t.Context())
			testutil.FailErr(t, "count unobserved", err)
			if coverage.BoundedDirectories != 1 {
				t.Fatalf("unobserved=%d", coverage.BoundedDirectories)
			}
			// The bound refused one directory; the rest of the tree is observed.
			if !slices.Contains(indexedPaths(t, reader, FileScope{Audience: HumanAudience, IncludeHidden: true}), "narrow/main.go") {
				t.Fatalf("a refused directory ended the walk: %v", indexedPaths(t, reader, FileScope{Audience: HumanAudience, IncludeHidden: true}))
			}
		})
	}
}

func TestIndexWalkBudgetLeavesTheRemainingFrontierUnobserved(t *testing.T) {
	c, root := indexFixture(t)
	c.SetScopes(testScopes{plane: sourcescope.Plane{Budgets: sandbox.SurveyBudgets{WalkEntries: 3}}})
	for _, dir := range []string{"alpha", "bravo", "charlie"} {
		writeIndexFile(t, root.Path, dir+"/main.go", "source")
	}
	reader := waitIndex(t, c, root)
	bounds := indexRefusals(t, c, root)
	if len(bounds) == 0 {
		t.Fatal("spent walk budget recorded no boundary")
	}
	for _, b := range bounds {
		if b.reason != sandbox.BoundaryWalkBudget {
			t.Fatalf("boundary=%+v want walk_budget", b)
		}
	}
	coverage, err := reader.Coverage(t.Context())
	testutil.FailErr(t, "count unobserved", err)
	if coverage.BoundedDirectories != len(bounds) {
		t.Fatalf("unobserved=%d rows=%d", coverage.BoundedDirectories, len(bounds))
	}
	// A spent budget still publishes a complete generation: it covers every
	// admitted path.
	if !reader.Status.Complete {
		t.Fatal("generation bounded by the walk budget never completed")
	}
}

func TestIndexIncrementalUpdateAndDirectoryDeletion(t *testing.T) {
	c, root := indexFixture(t)
	for i := range 600 {
		writeIndexFile(t, root.Path, fmt.Sprintf("generated/%04d.go", i), "x")
	}
	_ = waitIndex(t, c, root)

	writeIndexFile(t, root.Path, ".local.txt", "value")
	c.invalidateTrees(root.Path, []string{".local.txt"})
	reader := waitIndex(t, c, root)
	if !slices.Contains(indexedPaths(t, reader, FileScope{Audience: HumanAudience, IncludeHidden: true}), ".local.txt") {
		t.Fatal("added path missing from the index")
	}

	testutil.FailErr(t, "remove generated subtree", os.RemoveAll(filepath.Join(root.Path, "generated")))
	c.invalidateTrees(root.Path, []string{"generated"})
	reader = waitIndex(t, c, root)
	if got := indexedPaths(t, reader, FileScope{Audience: HumanAudience, IncludeHidden: true}); !slices.Equal(got, []string{".local.txt"}) {
		t.Fatalf("after removal=%v", got)
	}
	files, err := reader.FileCount(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true})
	testutil.FailErr(t, "root totals", err)
	if files != 1 {
		t.Fatalf("file count=%d", files)
	}
}

// Kind changes update file counts without inserting or deleting rows.
func TestIndexFileCountFollowsAPathChangingKind(t *testing.T) {
	c, root := indexFixture(t)
	writeIndexFile(t, root.Path, "thing/inner.go", "source")
	reader := waitIndex(t, c, root)
	files, err := reader.FileCount(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true})
	testutil.FailErr(t, "initial count", err)
	if files != 1 {
		t.Fatalf("initial count=%d", files)
	}

	testutil.FailErr(t, "replace directory with a file", os.RemoveAll(filepath.Join(root.Path, "thing")))
	writeIndexFile(t, root.Path, "thing", "now a file")
	c.invalidateTrees(root.Path, []string{"thing"})
	reader = waitIndex(t, c, root)
	if got := indexedPaths(t, reader, FileScope{Audience: HumanAudience, IncludeHidden: true}); !slices.Equal(got, []string{"thing"}) {
		t.Fatalf("after replacement=%v", got)
	}
	files, err = reader.FileCount(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true})
	testutil.FailErr(t, "count after replacement", err)
	if files != 1 {
		t.Fatalf("count after directory became a file=%d, want 1", files)
	}

	testutil.FailErr(t, "replace file with a directory", os.Remove(filepath.Join(root.Path, "thing")))
	writeIndexFile(t, root.Path, "thing/inner.go", "source")
	c.invalidateTrees(root.Path, []string{"thing"})
	reader = waitIndex(t, c, root)
	files, err = reader.FileCount(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true})
	testutil.FailErr(t, "count after reversal", err)
	if files != 1 {
		t.Fatalf("count after file became a directory=%d, want 1", files)
	}
}

func TestIndexIncrementalPassKeepsTheGenerationCovering(t *testing.T) {
	c, root := indexFixture(t)
	writeIndexFile(t, root.Path, "src/main.go", "source")
	first := waitIndex(t, c, root)
	revision := first.Status.Revision

	// Hidden-file writes preserve completed discovery.
	writeIndexFile(t, root.Path, ".output/cache/item", "generated")
	c.invalidateTrees(root.Path, []string{".output/cache/item"})
	next := waitIndex(t, c, root)
	if !next.Status.Complete {
		t.Fatal("an incremental pass left the generation reporting partial coverage")
	}
	if next.Status.Revision <= revision {
		t.Fatal("hidden write never reached the index Files reads")
	}
	if !slices.Contains(indexedPaths(t, next, FileScope{Audience: HumanAudience, IncludeHidden: true}), ".output/cache/item") {
		t.Fatal("hidden path missing from the Files projection")
	}
	if slices.Contains(indexedPaths(t, next, FileScope{Audience: HumanAudience}), ".output/cache/item") {
		t.Fatal("hidden path reached the code-search projection")
	}
}

func TestIndexRestartResumesAnInterruptedFrontier(t *testing.T) {
	c, root := indexFixture(t)
	writeIndexFile(t, root.Path, "old.go", "")
	_ = waitIndex(t, c, root)
	testutil.FailErr(t, "drain", c.Drain(t.Context()))

	s, err := c.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "index store", err)
	db, err := openTreeDB(t.Context(), s.file)
	testutil.FailErr(t, "index database", err)
	_, err = db.ExecContext(t.Context(),
		"UPDATE meta SET complete=0 WHERE id=1; INSERT OR IGNORE INTO frontier(path,deferred) VALUES('unfinished',0)")
	testutil.FailErr(t, "persist interrupted frontier", err)
	testutil.FailErr(t, "close database", db.Close())

	testutil.FailErr(t, "remove during downtime", os.Remove(filepath.Join(root.Path, "old.go")))
	writeIndexFile(t, root.Path, "new.go", "")
	restarted := New()
	t.Cleanup(func() { testutil.FailErr(t, "drain restarted catalog", restarted.Drain(context.Background())) })
	reader := waitIndex(t, restarted, root)
	if got := indexedPaths(t, reader, FileScope{Audience: HumanAudience, IncludeHidden: true}); !slices.Equal(got, []string{"new.go"}) {
		t.Fatalf("resumed index=%v", got)
	}
}

func TestIndexFaultSurvivesUnrelatedUpdate(t *testing.T) {
	c, root := indexFixture(t)
	writeIndexFile(t, root.Path, "good.go", "")
	_ = waitIndex(t, c, root)
	s, w := indexWalkFixture(t, c, root)
	testutil.FailErr(t, "record inaccessible directory", w.faultDir(t.Context(), "private", os.ErrPermission))
	testutil.FailErr(t, "publish fault", w.publish(t.Context()))
	if faults := indexFaults(t, s); !slices.Equal(faults, []string{"private"}) {
		t.Fatalf("faults=%v", faults)
	}
	info, err := os.Stat(filepath.Join(root.Path, "good.go"))
	testutil.FailErr(t, "file metadata", err)
	testutil.FailErr(t, "update unrelated file", w.writeLeaf(t.Context(), "good.go", info))
	testutil.FailErr(t, "publish unrelated update", w.publish(t.Context()))
	if faults := indexFaults(t, s); !slices.Equal(faults, []string{"private"}) {
		t.Fatalf("unrelated update erased the fault: %v", faults)
	}
	testutil.FailErr(t, "remove inaccessible path", w.faultDir(t.Context(), "private", os.ErrNotExist))
	testutil.FailErr(t, "publish removal", w.publish(t.Context()))
	if faults := indexFaults(t, s); len(faults) != 0 {
		t.Fatalf("removed path still faulted: %v", faults)
	}
}

func indexFaults(t *testing.T, s *indexStore) []string {
	t.Helper()
	db, err := openTreeDB(t.Context(), s.file)
	testutil.FailErr(t, "open index", err)
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(t.Context(), "SELECT path FROM faults ORDER BY path")
	testutil.FailErr(t, "read faults", err)
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var path string
		testutil.FailErr(t, "scan fault", rows.Scan(&path))
		out = append(out, path)
	}
	testutil.FailErr(t, "iterate faults", rows.Err())
	return out
}

func TestIndexAncestorsRunShallowestFirst(t *testing.T) {
	if got := indexAncestors("a/b/c"); !slices.Equal(got, []string{".", "a", "a/b", "a/b/c"}) {
		t.Fatalf("ancestors=%v", got)
	}
	if got := indexAncestors("."); !slices.Equal(got, []string{"."}) {
		t.Fatalf("root ancestors=%v", got)
	}
}

// Refusals come from persisted generation boundaries.
type indexRefusal struct {
	path   string
	reason sandbox.BoundaryReason
}

func indexRefusals(t *testing.T, c *Catalog, root Root) []indexRefusal {
	t.Helper()
	s, err := c.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "index store", err)
	db, err := openTreeDB(t.Context(), s.file)
	testutil.FailErr(t, "open index", err)
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(t.Context(),
		"SELECT path, refused FROM nodes WHERE refused<>'' ORDER BY depth, path")
	testutil.FailErr(t, "read boundaries", err)
	defer func() { _ = rows.Close() }()
	var out []indexRefusal
	for rows.Next() {
		var b indexRefusal
		var reason string
		testutil.FailErr(t, "scan boundary", rows.Scan(&b.path, &reason))
		b.reason = sandbox.BoundaryReason(reason)
		out = append(out, b)
	}
	testutil.FailErr(t, "iterate boundaries", rows.Err())
	return out
}

func TestIndexRejectsUnboundedPages(t *testing.T) {
	c, root := indexFixture(t)
	writeIndexFile(t, root.Path, "a.go", "source")
	reader := waitIndex(t, c, root)
	if _, err := reader.FilePage(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true}, "", TreeFilePageLimit+1); err == nil {
		t.Fatal("unbounded file page accepted")
	}
	if _, err := reader.NamedFiles(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true}, nil); err == nil {
		t.Fatal("empty named-file query accepted")
	}
}
