package sourcecatalog

import (
	"io"
	"path"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

type contentWatchFixture struct {
	directory            repochange.WatchObservation
	scope                repochange.WatchScopeObservation
	replacementDirectory repochange.WatchObservation
	replacementScope     repochange.WatchScopeObservation
	root                 repochange.WatchObservation
}

func newContentWatchFixture(t *testing.T) *contentWatchFixture {
	t.Helper()
	root := t.TempDir()
	markLiteralWatch(t, root)
	first := repochange.ObserveWatch(root, ".")
	firstScope := repochange.ObserveWatchScope(root)
	repochange.MarkCoverageCompleteForTest(root)
	return &contentWatchFixture{directory: first, scope: firstScope, replacementDirectory: repochange.ObserveWatch(root, "."), replacementScope: repochange.ObserveWatchScope(root), root: first}
}

func (w *contentWatchFixture) attach(cache *literalIndexCache) {
	cache.watchDirectory = func(_ string, dir string) repochange.WatchObservation {
		if dir == "src" {
			return w.directory
		}
		return w.root
	}
	cache.watchScope = func(string) repochange.WatchScopeObservation { return w.scope }
}

func (w *contentWatchFixture) rebindChild() {
	w.directory = w.replacementDirectory
	w.scope = w.replacementScope
}

func TestLiteralScopeRecoversFromChildWatchRebinding(t *testing.T) {
	root := t.TempDir()
	writeLiteralFixture(t, root, "src/noise.txt", "unrelated content")
	writeLiteralFixture(t, root, "stable.txt", "unrelated content")
	catalog := New()
	snapshot := observeRoot(t, catalog, root)
	watches := newContentWatchFixture(t)
	watches.attach(catalog.Literals.cache)
	var opens atomic.Int32
	query := LiteralQuery{RootID: "root", Require: litprefilter.AnyOf("absent-token"), Open: func(entry Entry) (io.ReadCloser, error) { opens.Add(1); return literalTestOpener(root)(entry) }}
	first, err := catalog.Literals.LiteralCandidates(t.Context(), snapshot, query)
	testutil.FailErr(t, "first scope", err)
	if len(first) != 0 || opens.Load() != 2 {
		t.Fatalf("first candidates=%d opens=%d", len(first), opens.Load())
	}
	watches.rebindChild()
	second, err := catalog.Literals.LiteralCandidates(t.Context(), snapshot, query)
	testutil.FailErr(t, "rebound scope", err)
	if len(second) != 0 || opens.Load() != 3 {
		t.Fatalf("rebound candidates=%d opens=%d; unrelated file must remain reusable", len(second), opens.Load())
	}
}

func TestPagedContentPreparationRecoversWithoutMetadataRevisionChange(t *testing.T) {
	catalog := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeTreeTestFile(t, root.Path, "src/noise.txt", "unrelated content")
	writeTreeTestFile(t, root.Path, "stable.txt", "unrelated content")
	reader := readyIndex(t, catalog, root)
	watches := newContentWatchFixture(t)
	watches.attach(catalog.Literals.cache)
	entries, err := reader.FilePage(t.Context(), FileScope{Audience: HumanAudience}, "", TreeFilePageLimit)
	testutil.FailErr(t, "metadata page", err)
	var opens atomic.Int32
	query := LiteralQuery{RootID: root.ID, IncludeKey: "recovery", Require: litprefilter.AnyOf("absent-token"), Open: func(entry Entry) (io.ReadCloser, error) { opens.Add(1); return literalTestOpener(root.Path)(entry) }}
	prepare := func() {
		catalog.Literals.IndexLiteralCandidates(t.Context(), reader, query, entries)
		reader.store.mu.Lock()
		build := reader.store.content[indexLiteralScopeKey(query)]
		reader.store.mu.Unlock()
		<-build.done
		page := catalog.Literals.IndexLiteralCandidates(t.Context(), reader, query, entries)
		if page.Warming || len(page.Candidates) != 0 {
			t.Fatalf("warm=%v candidates=%v", page.Warming, literalCandidatePaths(page.Candidates))
		}
	}
	prepare()
	revision := reader.Status.Revision
	watches.rebindChild()
	prepare()
	if reader.Status.Revision != revision || opens.Load() != 3 {
		t.Fatalf("revision=%d opens=%d", reader.Status.Revision, opens.Load())
	}
	for _, entry := range entries {
		if !catalog.Literals.cache.watchDirectory(root.Path, path.Dir(entry.Path)).Valid() {
			t.Fatal("fixture did not supply coverage")
		}
	}
}
