package sourcecatalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/testutil"
)

func observeRoot(t *testing.T, catalog *Catalog, root string) Snapshot {
	t.Helper()
	snapshot, err := catalog.Observe(context.Background(), "project", []Root{{ID: "root", Path: root}})
	testutil.FailErr(t, "observe root", err)
	return snapshot
}

func TestLiteralCandidatesCoverIgnoredAndHiddenTextAtRequestedAltitude(t *testing.T) {
	root := t.TempDir()
	writeLiteralFixture(t, root, ".ignored/generated.txt", "prefix CompleteNeedle suffix")
	writeLiteralFixture(t, root, "module/inside.txt", "CompleteNeedle")
	writeLiteralFixture(t, root, "sibling/outside.txt", "CompleteNeedle")

	catalog := New()
	snapshot := observeRoot(t, catalog, root)
	candidates, err := catalog.LiteralCandidates(context.Background(), snapshot, LiteralQuery{
		RootID: "root", Base: "module", Require: litprefilter.AnyOf("CompleteNeedle"), Open: literalTestOpener(root),
	})
	testutil.FailErr(t, "query module literals", err)
	got := literalCandidatePaths(candidates)
	if !slices.Equal(got, []string{"module/inside.txt"}) {
		t.Fatalf("module candidates = %v", got)
	}

	candidates, err = catalog.LiteralCandidates(context.Background(), snapshot, LiteralQuery{
		RootID: "root", Base: ".", Require: litprefilter.AnyOf("CompleteNeedle"), Open: literalTestOpener(root),
	})
	testutil.FailErr(t, "query root literals", err)
	got = literalCandidatePaths(candidates)
	if !slices.Equal(got, []string{".ignored/generated.txt", "module/inside.txt", "sibling/outside.txt"}) {
		t.Fatalf("root candidates = %v", got)
	}
}

func TestLiteralCandidatesMoveWithCatalogGeneration(t *testing.T) {
	root := t.TempDir()
	writeLiteralFixture(t, root, "source.txt", "before")
	catalog := New()
	query := LiteralQuery{RootID: "root", Base: ".", Require: litprefilter.AnyOf("before"), Open: literalTestOpener(root)}
	candidates, err := catalog.LiteralCandidates(context.Background(), observeRoot(t, catalog, root), query)
	testutil.FailErr(t, "query first generation", err)
	if got := literalCandidatePaths(candidates); !slices.Equal(got, []string{"source.txt"}) {
		t.Fatalf("first generation candidates = %v", literalCandidatePaths(candidates))
	}
	writeLiteralFixture(t, root, "added.txt", "after")
	catalog.InvalidateRoot(root)
	query.Require = litprefilter.AnyOf("before", "after")
	candidates, err = catalog.LiteralCandidates(context.Background(), observeRoot(t, catalog, root), query)
	testutil.FailErr(t, "query next generation", err)
	if got := literalCandidatePaths(candidates); !slices.Equal(got, []string{"added.txt", "source.txt"}) {
		t.Fatalf("next generation candidates = %v", got)
	}
}

func TestLiteralCandidatesInvalidateContentWhenMetadataIsUnchanged(t *testing.T) {
	root := t.TempDir()
	const rel = "source.txt"
	writeLiteralFixture(t, root, rel, "AlphaNeedle")
	info, err := os.Stat(filepath.Join(root, rel))
	testutil.FailErr(t, "stat initial fixture", err)
	catalog := New()
	query := LiteralQuery{RootID: "root", Base: ".", Require: litprefilter.AnyOf("AlphaNeedle"), Open: literalTestOpener(root)}
	candidates, err := catalog.LiteralCandidates(context.Background(), observeRoot(t, catalog, root), query)
	testutil.FailErr(t, "query initial content", err)
	if got := literalCandidatePaths(candidates); !slices.Equal(got, []string{rel}) {
		t.Fatalf("initial candidates = %v", got)
	}

	writeLiteralFixture(t, root, rel, "OmegaNeedle")
	testutil.FailErr(t, "restore fixture timestamp", os.Chtimes(filepath.Join(root, rel), info.ModTime(), info.ModTime()))
	catalog.InvalidateRoot(root, rel)
	query.Require = litprefilter.AnyOf("OmegaNeedle")
	candidates, err = catalog.LiteralCandidates(context.Background(), observeRoot(t, catalog, root), query)
	testutil.FailErr(t, "query invalidated content", err)
	if got := literalCandidatePaths(candidates); !slices.Equal(got, []string{rel}) {
		t.Fatalf("invalidated candidates = %v", got)
	}
}

// Known writes invalidate observations even while metadata refreshes.
func TestLiteralCandidatesFollowEditsUnderAStaleGeneration(t *testing.T) {
	root := t.TempDir()
	writeLiteralFixture(t, root, "edited.txt", "OldNeedle")
	writeLiteralFixture(t, root, "other.txt", "unrelated")
	catalog := New()
	stale := observeRoot(t, catalog, root)
	candidates, err := catalog.LiteralCandidates(context.Background(), stale, LiteralQuery{
		RootID: "root", Base: ".", Require: litprefilter.AnyOf("NewNeedle"), Open: literalTestOpener(root),
	})
	testutil.FailErr(t, "query before edit", err)
	if len(candidates) != 0 {
		t.Fatalf("candidates before edit = %v", literalCandidatePaths(candidates))
	}

	writeLiteralFixture(t, root, "edited.txt", "NewNeedle")
	catalog.InvalidateRoot(root, "edited.txt")
	candidates, err = catalog.LiteralCandidates(context.Background(), stale, LiteralQuery{
		RootID: "root", Base: ".", Require: litprefilter.AnyOf("NewNeedle"), Open: literalTestOpener(root),
	})
	testutil.FailErr(t, "query same generation after edit", err)
	if got := literalCandidatePaths(candidates); !slices.Equal(got, []string{"edited.txt"}) {
		t.Fatalf("candidates after edit = %v, want the edited file under the stale generation", got)
	}
}

// Unreadable files remain candidates for the caller to inspect.
func TestLiteralCandidatesKeepUnreadableFilesAsCandidates(t *testing.T) {
	root := t.TempDir()
	writeLiteralFixture(t, root, "open.txt", "needle")
	writeLiteralFixture(t, root, "locked.txt", "needle")
	writeLiteralFixture(t, root, "other.txt", "nothing")
	locked := filepath.Join(root, "locked.txt")
	testutil.FailErr(t, "chmod locked file", os.Chmod(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o600) })
	catalog := New()
	candidates, err := catalog.LiteralCandidates(context.Background(), observeRoot(t, catalog, root), LiteralQuery{
		RootID: "root", Base: ".", Require: litprefilter.AnyOf("needle"), Open: literalTestOpener(root),
	})
	testutil.FailErr(t, "query with an unreadable file", err)
	if got := literalCandidatePaths(candidates); !slices.Equal(got, []string{"locked.txt", "open.txt"}) {
		t.Fatalf("candidates = %v, want the unreadable file kept", got)
	}
}

// A file stays a candidate only when it may hold a literal from every clause.
func TestLiteralCandidatesHonourEveryClause(t *testing.T) {
	root := t.TempDir()
	writeLiteralFixture(t, root, "both.txt", "alphaword betaword")
	writeLiteralFixture(t, root, "alpha.txt", "alphaword only")
	writeLiteralFixture(t, root, "gamma.txt", "gammaword betaword")
	writeLiteralFixture(t, root, "none.txt", "nothing")
	catalog := New()
	snapshot := observeRoot(t, catalog, root)
	cases := []struct {
		name    string
		require litprefilter.Requirement
		want    []string
	}{
		{"any", litprefilter.AnyOf("alphaword", "betaword"), []string{"alpha.txt", "both.txt", "gamma.txt"}},
		{"all", litprefilter.AllOf("alphaword", "betaword"), []string{"both.txt"}},
		{"clauses", litprefilter.Requirement{Clauses: []litprefilter.Clause{
			litprefilter.AnyOf("alphaword", "gammaword").Clauses[0], litprefilter.AnyOf("betaword").Clauses[0],
		}}, []string{"both.txt", "gamma.txt"}},
	}
	for _, tc := range cases {
		query := LiteralQuery{RootID: "root", Base: ".", Require: tc.require, Open: literalTestOpener(root)}
		candidates, err := catalog.LiteralCandidates(context.Background(), snapshot, query)
		testutil.FailErr(t, tc.name+" query", err)
		if got := literalCandidatePaths(candidates); !slices.Equal(got, tc.want) {
			t.Fatalf("%s candidates = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestLiteralCandidatesRequireAReadyGeneration(t *testing.T) {
	catalog := New()
	_, err := catalog.LiteralCandidates(context.Background(), Snapshot{State: StateWarming}, LiteralQuery{
		RootID: "root", Require: litprefilter.AnyOf("needle"), Open: literalTestOpener(t.TempDir()),
	})
	if err == nil {
		t.Fatal("warming generation was accepted")
	}
}

// Literal and regexp candidates include Unicode case-fold equivalents.
func TestLiteralCandidatesFoldCase(t *testing.T) {
	root := t.TempDir()
	writeLiteralFixture(t, root, "mixed.txt", "CompleteNeedle")
	writeLiteralFixture(t, root, "kelvin.txt", "Kelvin scale")
	writeLiteralFixture(t, root, "istanbul.txt", "İstanbul")
	writeLiteralFixture(t, root, "none.txt", "nothing here")
	catalog := New()
	snapshot := observeRoot(t, catalog, root)
	cases := map[string][]string{
		"completeneedle": {"mixed.txt"},
		"COMPLETENEEDLE": {"mixed.txt"},
		"kelvin":         {"kelvin.txt"},
		"istanbul":       {"istanbul.txt"},
	}
	for literal, want := range cases {
		candidates, err := catalog.LiteralCandidates(context.Background(), snapshot, LiteralQuery{
			RootID: "root", Base: ".", Require: litprefilter.AnyOf(literal), Open: literalTestOpener(root),
		})
		testutil.FailErr(t, "query folded literal "+literal, err)
		if got := literalCandidatePaths(candidates); !slices.Equal(got, want) {
			t.Fatalf("candidates for %q = %v, want %v", literal, got, want)
		}
	}
}

func TestLiteralCandidatesSkipNULWithoutDroppingOtherText(t *testing.T) {
	root := t.TempDir()
	writeLiteralFixture(t, root, "binary.dat", "before\x00CompleteNeedle")
	writeLiteralFixture(t, root, "control.txt", "before\x1bCompleteNeedle")
	writeLiteralFixture(t, root, "source.unknown", "CompleteNeedle")
	catalog := New()
	candidates, err := catalog.LiteralCandidates(context.Background(), observeRoot(t, catalog, root), LiteralQuery{
		RootID: "root", Base: ".", Require: litprefilter.AnyOf("CompleteNeedle"), Open: literalTestOpener(root),
	})
	testutil.FailErr(t, "query text candidates", err)
	if got := literalCandidatePaths(candidates); !slices.Equal(got, []string{"control.txt", "source.unknown"}) {
		t.Fatalf("text candidates = %v", got)
	}
}

// A multi-byte rune split by the reader's chunk boundary is folded whole.
func TestLiteralBloomFoldsRunesAcrossReadBoundaries(t *testing.T) {
	body := strings.Repeat("x", 64*1024-1) + "İstanbul"
	bloom, searchable, err := buildLiteralBloomReader(context.Background(), strings.NewReader(body), int64(len(body)))
	testutil.FailErr(t, "build bloom", err)
	if !searchable {
		t.Fatal("valid UTF-8 was not searchable")
	}
	if !bloom.mayContain(foldLiteral("istanbul")) {
		t.Fatal("rune split at the chunk boundary was not folded")
	}
}

func TestLiteralCandidatesCoalesceConcurrentScopeBuilds(t *testing.T) {
	root := t.TempDir()
	const fileCount = 200
	for i := range fileCount {
		writeLiteralFixture(t, root, fmt.Sprintf("pkg/file-%03d.go", i), fmt.Sprintf("term%d common", i))
	}
	catalog := New()
	snapshot := observeRoot(t, catalog, root)
	var opens atomic.Int64
	opener := func(entry Entry) (io.ReadCloser, error) {
		opens.Add(1)
		return os.Open(filepath.Join(root, filepath.FromSlash(entry.Path))) // #nosec G304 -- test fixture root
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for queryIndex := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := catalog.LiteralCandidates(context.Background(), snapshot, LiteralQuery{
				RootID: "root", Base: ".", Require: litprefilter.AnyOf(fmt.Sprintf("term%d", queryIndex)),
				IncludeKey: "same-scope", Open: opener,
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		testutil.FailErr(t, "concurrent literal query", err)
	}
	if got := opens.Load(); got != fileCount {
		t.Fatalf("content opens = %d, want one shared %d-file index build", got, fileCount)
	}
}

// The next generation's scope index reuses per-file blooms and rereads only
// the files whose metadata changed or whose paths a write invalidated.
func TestLiteralCandidatesReuseFileBloomsAcrossGenerations(t *testing.T) {
	root := t.TempDir()
	writeLiteralFixture(t, root, "keep.txt", "steady")
	writeLiteralFixture(t, root, "edit.txt", "steady")
	catalog := New()
	var opened []string
	var mu sync.Mutex
	opener := func(entry Entry) (io.ReadCloser, error) {
		mu.Lock()
		opened = append(opened, entry.Path)
		mu.Unlock()
		return os.Open(filepath.Join(root, filepath.FromSlash(entry.Path))) // #nosec G304 -- test fixture root
	}
	query := LiteralQuery{RootID: "root", Base: ".", Require: litprefilter.AnyOf("steady"), Open: opener}
	_, err := catalog.LiteralCandidates(context.Background(), observeRoot(t, catalog, root), query)
	testutil.FailErr(t, "first build", err)
	mu.Lock()
	opened = opened[:0]
	mu.Unlock()

	writeLiteralFixture(t, root, "edit.txt", "changed")
	catalog.InvalidateRoot(root, "edit.txt")
	next := observeRoot(t, catalog, root)
	_, err = catalog.LiteralCandidates(context.Background(), next, query)
	testutil.FailErr(t, "rebuild for the next generation", err)
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(opened, []string{"edit.txt"}) {
		t.Fatalf("reread = %v, want only the edited file", opened)
	}
}

// A pathless event cannot prove that metadata identifies the same bytes.
func TestLiteralCandidatesPathlessInvalidationRevalidatesFileBlooms(t *testing.T) {
	root := t.TempDir()
	writeLiteralFixture(t, root, "src/a.txt", "needle")
	writeLiteralFixture(t, root, "src/b.txt", "other")
	catalog := New()
	var opens atomic.Int64
	opener := func(entry Entry) (io.ReadCloser, error) {
		opens.Add(1)
		return os.Open(filepath.Join(root, filepath.FromSlash(entry.Path))) // #nosec G304 -- test fixture root
	}
	query := LiteralQuery{RootID: "root", Base: ".", Require: litprefilter.AnyOf("needle"), Open: opener}
	stale := observeRoot(t, catalog, root)
	_, err := catalog.LiteralCandidates(context.Background(), stale, query)
	testutil.FailErr(t, "first build", err)
	before := opens.Load()

	catalog.InvalidateRoot(root)
	candidates, err := catalog.LiteralCandidates(context.Background(), stale, query)
	testutil.FailErr(t, "query under the dirty generation", err)
	if got := literalCandidatePaths(candidates); !slices.Equal(got, []string{"src/a.txt", "src/b.txt"}) {
		t.Fatalf("dirty-root candidates = %v, want every file", got)
	}
	next := observeRoot(t, catalog, root)
	candidates, err = catalog.LiteralCandidates(context.Background(), next, query)
	testutil.FailErr(t, "rebuild for the next generation", err)
	if got := literalCandidatePaths(candidates); !slices.Equal(got, []string{"src/a.txt"}) {
		t.Fatalf("next-generation candidates = %v", got)
	}
	if opens.Load()-before != 2 {
		t.Fatalf("pathless invalidation reread %d files, want 2", opens.Load()-before)
	}
}

// Writes outside the projection preserve its cached observations.
func TestLiteralCandidatesKeepIndexAcrossUnrelatedWrites(t *testing.T) {
	root := t.TempDir()
	writeLiteralFixture(t, root, "src/a.txt", "needle")
	writeLiteralFixture(t, root, "src/b.txt", "other")
	catalog := New()
	var opens atomic.Int64
	opener := func(entry Entry) (io.ReadCloser, error) {
		opens.Add(1)
		return os.Open(filepath.Join(root, filepath.FromSlash(entry.Path))) // #nosec G304 -- test fixture root
	}
	query := LiteralQuery{RootID: "root", Base: ".", Require: litprefilter.AnyOf("needle"), Open: opener}
	snapshot := observeRoot(t, catalog, root)
	_, err := catalog.LiteralCandidates(context.Background(), snapshot, query)
	testutil.FailErr(t, "first build", err)
	before := opens.Load()

	writeLiteralFixture(t, root, "build/out.log", "log line")
	catalog.InvalidateRoot(root, "build/out.log")
	candidates, err := catalog.LiteralCandidates(context.Background(), snapshot, query)
	testutil.FailErr(t, "query after unrelated write", err)
	if got := literalCandidatePaths(candidates); !slices.Equal(got, []string{"src/a.txt"}) {
		t.Fatalf("candidates = %v", got)
	}
	if opens.Load() != before {
		t.Fatalf("unrelated write reopened %d files", opens.Load()-before)
	}
}

// A write during a read prevents publication of that bloom.
func TestLiteralBloomReadAcrossAWriteIsNotRetained(t *testing.T) {
	root := t.TempDir()
	writeLiteralFixture(t, root, "racy.txt", "before")
	catalog := New()
	snapshot := observeRoot(t, catalog, root)
	var opens atomic.Int64
	rewriteOnce := sync.OnceFunc(func() {
		// Build workers cannot call testing.T.
		_ = os.WriteFile(filepath.Join(root, "racy.txt"), []byte("after and longer"), 0o600)
	})
	opener := func(entry Entry) (io.ReadCloser, error) {
		opens.Add(1)
		f, err := os.Open(filepath.Join(root, filepath.FromSlash(entry.Path))) // #nosec G304 -- test fixture root
		rewriteOnce()
		return f, err
	}
	query := LiteralQuery{RootID: "root", Base: ".", Require: litprefilter.AnyOf("before"), Open: opener}
	_, err := catalog.LiteralCandidates(context.Background(), snapshot, query)
	testutil.FailErr(t, "build across the write", err)

	// Same generation metadata again: a retained bloom would answer from cache.
	query.IncludeKey = "second-scope"
	query.Include = func(Entry) bool { return true }
	_, err = catalog.LiteralCandidates(context.Background(), snapshot, query)
	testutil.FailErr(t, "rebuild after the racy read", err)
	if got := opens.Load(); got != 2 {
		t.Fatalf("opens = %d, want the racy read discarded and the file reread", got)
	}
}

func TestLiteralCandidatesApplyIncludeBeforeOpeningContent(t *testing.T) {
	root := t.TempDir()
	writeLiteralFixture(t, root, "allowed/source.go", "needle")
	writeLiteralFixture(t, root, "private/secret.txt", "needle")
	var opened []string
	catalog := New()
	candidates, err := catalog.LiteralCandidates(context.Background(), observeRoot(t, catalog, root), LiteralQuery{
		RootID: "root", Base: ".",
		Require: litprefilter.AnyOf("needle"), IncludeKey: "allowed-only",
		Include: func(entry Entry) bool { return strings.HasPrefix(entry.Path, "allowed/") },
		Open: func(entry Entry) (io.ReadCloser, error) {
			opened = append(opened, entry.Path)
			return os.Open(filepath.Join(root, filepath.FromSlash(entry.Path))) // #nosec G304 -- test fixture root
		},
	})
	testutil.FailErr(t, "query authorized literals", err)
	if !slices.Equal(opened, []string{"allowed/source.go"}) {
		t.Fatalf("opened = %v, want included content only", opened)
	}
	if got := literalCandidatePaths(candidates); !slices.Equal(got, []string{"allowed/source.go"}) {
		t.Fatalf("candidates = %v", got)
	}
}

func TestLiteralCandidatesCanceledWaiterDoesNotAbortSharedBuild(t *testing.T) {
	root := t.TempDir()
	writeLiteralFixture(t, root, "source.go", "CompleteNeedle")
	catalog := New()
	snapshot := observeRoot(t, catalog, root)
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	var opens atomic.Int64
	query := LiteralQuery{
		RootID: "root", Base: ".",
		Require: litprefilter.AnyOf("CompleteNeedle"), IncludeKey: "same-scope",
		Open: func(entry Entry) (io.ReadCloser, error) {
			opens.Add(1)
			startOnce.Do(func() { close(started) })
			<-release
			return os.Open(filepath.Join(root, filepath.FromSlash(entry.Path))) // #nosec G304 -- test fixture root
		},
	}

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstErr := make(chan error, 1)
	go func() {
		_, err := catalog.LiteralCandidates(firstCtx, snapshot, query)
		firstErr <- err
	}()
	<-started

	secondResult := make(chan error, 1)
	go func() {
		candidates, err := catalog.LiteralCandidates(context.Background(), snapshot, query)
		if err == nil && !slices.Equal(literalCandidatePaths(candidates), []string{"source.go"}) {
			err = fmt.Errorf("candidates = %v", literalCandidatePaths(candidates))
		}
		secondResult <- err
	}()
	testutil.WaitFor(t, time.Second, func() bool {
		catalog.literals.mu.Lock()
		defer catalog.literals.mu.Unlock()
		for _, record := range catalog.literals.records {
			if record.waiters == 2 {
				return true
			}
		}
		return false
	})
	cancelFirst()
	if err := <-firstErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("first query error = %v, want context canceled", err)
	}
	close(release)
	testutil.FailErr(t, "shared build after first waiter canceled", <-secondResult)
	if got := opens.Load(); got != 1 {
		t.Fatalf("content opens = %d, want one shared build", got)
	}
}

func foldLiteral(literal string) []byte {
	return litprefilter.AppendCanonicalFold(nil, []byte(literal))
}

func writeLiteralFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	testutil.FailErr(t, "create fixture directory", os.MkdirAll(filepath.Dir(abs), 0o700))
	testutil.FailErr(t, "write fixture", os.WriteFile(abs, []byte(content), 0o600))
}

func literalTestOpener(root string) func(Entry) (io.ReadCloser, error) {
	return func(entry Entry) (io.ReadCloser, error) {
		return os.Open(filepath.Join(root, filepath.FromSlash(entry.Path))) // #nosec G304 -- test fixture root
	}
}

func literalCandidatePaths(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Path)
	}
	slices.Sort(out)
	return out
}

func TestLiteralBudgetFallsBackWithoutOpeningContent(t *testing.T) {
	catalog := New()
	snapshot := Snapshot{State: StateReady, Revision: 1, Roots: []Root{{ID: "root", Path: t.TempDir()}}}
	for i := range 1024 {
		snapshot.Entries = append(snapshot.Entries, Entry{RootID: "root", Path: fmt.Sprintf("%04d.txt", i), Size: 1 << 30})
	}
	candidates, err := catalog.LiteralCandidates(t.Context(), snapshot, LiteralQuery{RootID: "root", Base: ".", Require: litprefilter.AnyOf("needle"), Open: func(Entry) (io.ReadCloser, error) {
		t.Error("oversized scope opened content")
		return nil, os.ErrNotExist
	}})
	testutil.FailErr(t, "query oversized scope", err)
	if len(candidates) != len(snapshot.Entries) {
		t.Fatalf("lost coverage: %d of %d candidates", len(candidates), len(snapshot.Entries))
	}
}

func TestLiteralBuildStopsWhenLastWaiterLeaves(t *testing.T) {
	cache := newLiteralIndexCache()
	started := make(chan struct{})
	stopped := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := cache.load(ctx, "key", "root", func(buildCtx context.Context) (*literalScopeIndex, error) {
			close(started)
			<-buildCtx.Done()
			close(stopped)
			return nil, buildCtx.Err()
		})
		result <- err
	}()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("caller error=%v", err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("abandoned build continued")
	}
}

func TestLiteralCacheAccountsForNonTextFiles(t *testing.T) {
	cache := newLiteralIndexCache()
	cache.files.store("root", "binary.dat", literalFileCacheEntry{})
	if cache.files.bytes <= int64(len("root\x00binary.dat")) {
		t.Fatalf("binary metadata has no cost: %d", cache.files.bytes)
	}
	before := cache.files.bytes
	cache.files.store("root", "binary.dat", literalFileCacheEntry{})
	if cache.files.bytes != before || cache.files.count != 1 {
		t.Fatalf("replacement double charged: %d vs %d (count %d)", cache.files.bytes, before, cache.files.count)
	}
	_, err := cache.load(t.Context(), "large", "root", func(context.Context) (*literalScopeIndex, error) {
		return &literalScopeIndex{bytes: literalIndexByteCap + 1}, nil
	})
	testutil.FailErr(t, "serve oversized transient result", err)
	if cache.bytes != 0 || len(cache.records) != 0 {
		t.Fatalf("oversized index retained: bytes=%d records=%d", cache.bytes, len(cache.records))
	}
}

func TestInvalidatedNonTextFileRemainsSearchable(t *testing.T) {
	root := t.TempDir()
	writeLiteralFixture(t, root, "changed.dat", "\x00binary")
	catalog := New()
	snapshot := observeRoot(t, catalog, root)
	query := LiteralQuery{RootID: "root", Base: ".", Require: litprefilter.AnyOf("needle"), Open: literalTestOpener(root)}
	candidates, err := catalog.LiteralCandidates(t.Context(), snapshot, query)
	testutil.FailErr(t, "index non-text source", err)
	if len(candidates) != 0 {
		t.Fatalf("binary candidates=%v", candidates)
	}
	writeLiteralFixture(t, root, "changed.dat", "needle")
	catalog.literals.invalidate(root, []string{"changed.dat"})
	candidates, err = catalog.LiteralCandidates(t.Context(), snapshot, query)
	testutil.FailErr(t, "search invalidated generation", err)
	if !slices.Equal(literalCandidatePaths(candidates), []string{"changed.dat"}) {
		t.Fatalf("changed text omitted: %v", candidates)
	}
}

func TestLiteralRootInvalidationDropsUncertainFileBlooms(t *testing.T) {
	root := t.TempDir()
	writeLiteralFixture(t, root, "a.txt", "old text")
	catalog := New()
	query := LiteralQuery{RootID: "root", Base: ".", Require: litprefilter.AnyOf("new text"), Open: literalTestOpener(root)}
	initial := observeRoot(t, catalog, root)
	_, err := catalog.LiteralCandidates(t.Context(), initial, query)
	testutil.FailErr(t, "build initial index", err)
	info, err := os.Stat(filepath.Join(root, "a.txt"))
	testutil.FailErr(t, "stat source", err)
	writeLiteralFixture(t, root, "a.txt", "new text")
	testutil.FailErr(t, "preserve modification time", os.Chtimes(filepath.Join(root, "a.txt"), info.ModTime(), info.ModTime()))
	catalog.InvalidateRoot(root, ".")
	next := observeRoot(t, catalog, root)
	candidates, err := catalog.LiteralCandidates(t.Context(), next, query)
	testutil.FailErr(t, "search after root invalidation", err)
	if got := literalCandidatePaths(candidates); !slices.Equal(got, []string{"a.txt"}) {
		t.Fatalf("candidates=%v", got)
	}
}
