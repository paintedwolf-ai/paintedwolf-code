package sourcecatalog

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

// reconcileWork runs one full walk against the stored generation and reports
// how much of it reached the database.
func reconcileWork(t *testing.T, s *summaryStore) (visited, written int) {
	t.Helper()
	db, err := openTreeDB(t.Context(), s.file)
	testutil.FailErr(t, "open tree database", err)
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "reconcile transaction", err)
	defer func() { _ = tx.Rollback() }()
	w, err := newSummaryWriter(t.Context(), tx, s)
	testutil.FailErr(t, "reconcile writer", err)
	defer w.close()
	_, err = w.scan(t.Context(), ".")
	testutil.FailErr(t, "full walk", err)
	testutil.FailErr(t, "sweep", w.sweep(t.Context(), "."))
	testutil.FailErr(t, "commit reconcile", tx.Commit())
	return w.RowsVisited, w.RowsWritten
}

// A full walk over an unchanged tree compares every node and writes none of
// them; a single edit writes the file and the ancestors whose totals moved.
func TestTreeFullWalkWritesOnlyChangedRows(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	for _, rel := range []string{"a/one.go", "a/two.go", "b/three.go", "top.md"} {
		writeTreeTestFile(t, root.Path, rel, "material")
	}
	store := summaryStoreFor(t, c, root, TreeScope{Key: "all"})
	r := readySummary(t, c, root, TreeScope{Key: "all"})
	testutil.FailErr(t, "release reader", r.Close())
	visited, written := reconcileWork(t, store)
	if visited != 7 || written != 0 {
		t.Fatalf("unchanged tree: visited=%d written=%d, want 7 visited and nothing written", visited, written)
	}
	writeTreeTestFile(t, root.Path, "a/one.go", "longer material")
	visited, written = reconcileWork(t, store)
	if visited != 7 || written != 3 {
		t.Fatalf("one edit: visited=%d written=%d, want the file, its directory, and the root", visited, written)
	}
	fresh := summaryReaderFor(t, store)
	defer func() { _ = fresh.Close() }()
	n, err := fresh.Node(t.Context(), ".")
	testutil.FailErr(t, "root totals", err)
	if n.Files != 4 || n.Bytes != int64(3*len("material")+len("longer material")) {
		t.Fatalf("root totals after edit=%+v", n)
	}
}

// Rows the walk did not reach are swept, terms included, without rewriting
// the rows it did reach.
func TestTreeFullWalkSweepsVanishedRows(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeTreeTestFile(t, root.Path, "gone/deep/old.go", "old")
	writeTreeTestFile(t, root.Path, "kept.go", "kept")
	store := summaryStoreFor(t, c, root, TreeScope{Key: "all"})
	r := readySummary(t, c, root, TreeScope{Key: "all"})
	testutil.FailErr(t, "release reader", r.Close())
	testutil.FailErr(t, "remove subtree", os.RemoveAll(filepath.Join(root.Path, "gone")))
	visited, written := reconcileWork(t, store)
	if visited != 2 || written != 1 {
		t.Fatalf("after removal: visited=%d written=%d, want only the root rewritten", visited, written)
	}
	fresh := summaryReaderFor(t, store)
	defer func() { _ = fresh.Close() }()
	if _, err := fresh.Node(t.Context(), "gone/deep/old.go"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("vanished file survived the sweep: err=%v", err)
	}
	if _, err := fresh.Node(t.Context(), "gone"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("vanished directory survived the sweep: err=%v", err)
	}
	var orphanTerms int
	testutil.FailErr(t, "count orphan terms", fresh.tx.QueryRowContext(t.Context(), "SELECT count(*) FROM terms WHERE path LIKE 'gone%'").Scan(&orphanTerms))
	if orphanTerms != 0 {
		t.Fatalf("%d terms outlived their nodes", orphanTerms)
	}
	n, err := fresh.Node(t.Context(), ".")
	testutil.FailErr(t, "root totals", err)
	if n.Files != 1 || n.Children != 1 {
		t.Fatalf("root totals after sweep=%+v", n)
	}
}

// A committed reconciliation folds its log back into the main file; the log
// left on disk is empty rather than the size of the generation.
func TestTreeReconcileTruncatesWriteAheadLog(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	for i := range 64 {
		writeTreeTestFile(t, root.Path, filepath.Join("dir", string(rune('a'+i%26)), "file"+string(rune('a'+i%26))+".go"), "package material")
	}
	first := readySummary(t, c, root, TreeScope{Key: "all"})
	testutil.FailErr(t, "release pinned reader", first.Close())
	writeTreeTestFile(t, root.Path, "extra.go", "more")
	c.InvalidateRoot(root.Path)
	next := readySummary(t, c, root, TreeScope{Key: "all"})
	if next.Status.Revision <= first.Status.Revision {
		t.Fatalf("full reconcile did not advance: %+v", next.Status)
	}
	info, err := os.Stat(summaryStoreFor(t, c, root, TreeScope{Key: "all"}).file + "-wal")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		testutil.FailErr(t, "stat write-ahead log", err)
	}
	if err == nil && info.Size() != 0 {
		t.Fatalf("write-ahead log holds %d bytes after reconcile; want it truncated", info.Size())
	}
}

func TestTreeStoreRetentionRemovesOnlyUnusedGenerations(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeTreeTestFile(t, root.Path, "a.go", "package source")
	_ = readySummary(t, c, root, TreeScope{Key: "all"})
	live := summaryStoreFor(t, c, root, TreeScope{Key: "all"})
	stale := time.Now().Add(-2 * defaultTreeStorePolicy().retention)
	testutil.FailErr(t, "age live generation", os.Chtimes(live.file, stale, stale))
	orphan := filepath.Join(c.treeDir, "0000deadbeef"+treeFileSuffix)
	for _, path := range append([]string{orphan}, treeSidecarPaths(orphan)...) {
		testutil.FailErr(t, "write orphan generation", os.WriteFile(path, []byte("stale"), 0o600))
		testutil.FailErr(t, "age orphan generation", os.Chtimes(path, stale, stale))
	}
	recent := filepath.Join(c.treeDir, "0000cafef00d"+treeFileSuffix)
	recentDB, err := openTreeDB(t.Context(), recent)
	testutil.FailErr(t, "open recent generation", err)
	testutil.FailErr(t, "close recent generation", recentDB.Close())
	removed, err := c.ReconcileTreeStores(t.Context())
	testutil.FailErr(t, "reconcile generations", err)
	if removed != 1 {
		t.Fatalf("removed %d generations, want the one stale orphan", removed)
	}
	for _, path := range append([]string{orphan}, treeSidecarPaths(orphan)...) {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale orphan %s survived: err=%v", path, err)
		}
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("recent generation removed: %v", err)
	}
	if _, err := os.Stat(live.file); err != nil {
		t.Fatalf("open generation removed: %v", err)
	}
}

// An open stamps use on the file, so a scope that is only ever read still
// counts as in use for retention.
func TestTreeOpenStampsUseForRetention(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeTreeTestFile(t, root.Path, "a.go", "package source")
	_ = readySummary(t, c, root, TreeScope{Key: "all"})
	store := summaryStoreFor(t, c, root, TreeScope{Key: "all"})
	stale := time.Now().Add(-2 * defaultTreeStorePolicy().retention)
	testutil.FailErr(t, "age generation", os.Chtimes(store.file, stale, stale))
	store.mu.Lock()
	store.touched = time.Time{}
	store.mu.Unlock()
	_ = readySummary(t, c, root, TreeScope{Key: "all"})
	info, err := os.Stat(store.file)
	testutil.FailErr(t, "stat generation", err)
	if !info.ModTime().After(stale.Add(defaultTreeStorePolicy().retention)) {
		t.Fatalf("open did not stamp use: mtime=%s", info.ModTime())
	}
}

func TestClearTreeStoresRebuildsOnNextOpen(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeTreeTestFile(t, root.Path, "a.go", "package source")
	first := readySummary(t, c, root, TreeScope{Key: "all"})
	file := summaryStoreFor(t, c, root, TreeScope{Key: "all"}).file
	testutil.FailErr(t, "release reader", first.Close())
	testutil.FailErr(t, "clear generations", c.ClearTreeStores(context.Background(), nil))
	testutil.FailErr(t, "remove generation files", removeTreeStore(file))
	rebuilt := readySummary(t, c, root, TreeScope{Key: "all"})
	n, err := rebuilt.Node(t.Context(), ".")
	testutil.FailErr(t, "rebuilt totals", err)
	if n.Files != 1 {
		t.Fatalf("rebuilt generation=%+v", n)
	}
}

func TestClearTreeStoresExcludesNewAdmissionAndRetiresCapturedStore(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	stale, err := c.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "create initial store", err)
	entered := make(chan struct{})
	release := make(chan struct{})
	cleared := make(chan error, 1)
	go func() {
		cleared <- c.ClearTreeStores(t.Context(), func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	if _, err := openStore(t.Context(), stale, 0, c.broker); !errors.Is(err, pagedview.ErrExpired) {
		t.Fatalf("captured store admitted work during clear: %v", err)
	}
	opened := make(chan *indexStore, 1)
	openErr := make(chan error, 1)
	go func() {
		store, err := c.indexStore(t.Context(), "p", root)
		opened <- store
		openErr <- err
	}()
	select {
	case <-opened:
		t.Fatal("new store admitted before catalog storage clear completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	testutil.FailErr(t, "clear tree stores", <-cleared)
	next := <-opened
	testutil.FailErr(t, "open replacement store", <-openErr)
	if next == stale {
		t.Fatal("clear reused retired store")
	}
}

func TestReleaseTreeRootRemovesObsoleteCache(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeTreeTestFile(t, root.Path, "a.go", "package source")
	reader := readySummary(t, c, root, TreeScope{Key: "all"})
	testutil.FailErr(t, "close reader", reader.Close())
	file := summaryStoreFor(t, c, root, TreeScope{Key: "all"}).file

	testutil.FailErr(t, "release detached root", c.ReleaseTreeRoot(t.Context(), root.Path))
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("detached root cache remains: %v", err)
	}
}

func TestClearTreeStoresJoinsPreviouslyAdmittedWriter(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	store, err := c.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "create store", err)
	unwrite, err := store.write(t.Context())
	testutil.FailErr(t, "admit writer", err)
	clearing := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- c.ClearTreeStores(t.Context(), func() error {
			close(clearing)
			return nil
		})
	}()
	select {
	case <-clearing:
		t.Fatal("storage clear overtook an admitted writer")
	case <-time.After(20 * time.Millisecond):
	}
	unwrite()
	select {
	case <-clearing:
	case <-t.Context().Done():
		t.Fatal("storage clear did not join released writer")
	}
	testutil.FailErr(t, "clear tree stores", <-done)
	if _, err := store.write(t.Context()); !errors.Is(err, pagedview.ErrExpired) {
		t.Fatalf("retired store admitted writer: %v", err)
	}
}

func TestTreeStoreBudgetIncludesOpenStoreAndEvictsClosedLRU(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeTreeTestFile(t, root.Path, "a.go", "package source")
	_ = readySummary(t, c, root, TreeScope{Key: "all"})
	live := summaryStoreFor(t, c, root, TreeScope{Key: "all"})
	old := filepath.Join(c.treeDir, "old.db")
	recent := filepath.Join(c.treeDir, "recent.db")
	for _, file := range []string{old, recent} {
		database, err := openTreeDB(t.Context(), file)
		testutil.FailErr(t, "open closed fixture", err)
		_, err = database.ExecContext(t.Context(), "CREATE TABLE fixture(body BLOB); INSERT INTO fixture VALUES(zeroblob(65536))")
		testutil.FailErr(t, "populate closed fixture", err)
		testutil.FailErr(t, "close fixture", database.Close())
	}
	aged := time.Now().Add(-time.Hour)
	testutil.FailErr(t, "age LRU fixture", os.Chtimes(old, aged, aged))
	budget := treeStoreBytes(live.file) + treeStoreBytes(recent)
	removed, err := c.reconcileTreeStores(t.Context(), treeStorePolicy{retention: 24 * time.Hour, maxBytes: budget, vacuumPages: 2048})
	testutil.FailErr(t, "reconcile shared budget", err)
	if removed != 1 {
		t.Fatalf("removed=%d, want only closed LRU", removed)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("closed LRU survived open-store usage")
	}
	for _, file := range []string{live.file, recent} {
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("retained store removed: %s: %v", file, err)
		}
	}
}

func TestRetainedTreeStoreReturnsFreePagesToFilesystem(t *testing.T) {
	c := treeTestCatalog(t)
	file := filepath.Join(c.treeDir, "retained.db")
	database, err := openTreeDB(t.Context(), file)
	testutil.FailErr(t, "open vacuum fixture", err)
	var mode int
	testutil.FailErr(t, "read vacuum mode", database.QueryRowContext(t.Context(), "PRAGMA auto_vacuum").Scan(&mode))
	if mode != 2 {
		t.Fatalf("new store vacuum mode=%d, want incremental", mode)
	}
	_, err = database.ExecContext(t.Context(), "CREATE TABLE fixture(body BLOB); INSERT INTO fixture VALUES(zeroblob(4194304)); PRAGMA wal_checkpoint(TRUNCATE)")
	testutil.FailErr(t, "populate vacuum fixture", err)
	before := treeStoreBytes(file)
	_, err = database.ExecContext(t.Context(), "DELETE FROM fixture; PRAGMA wal_checkpoint(TRUNCATE)")
	testutil.FailErr(t, "free fixture pages", err)
	testutil.FailErr(t, "close vacuum fixture", database.Close())
	_, err = c.ReconcileTreeStores(t.Context())
	testutil.FailErr(t, "reclaim free pages", err)
	if after := treeStoreBytes(file); after >= before {
		t.Fatalf("retained store did not shrink: before=%d after=%d", before, after)
	}
}
