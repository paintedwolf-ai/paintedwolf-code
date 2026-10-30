package sourcecatalog

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/testutil"
)

func preparedLiteralPage(t *testing.T, c *Catalog, reader *IndexReader, query LiteralQuery, entries []Entry) LiteralPage {
	t.Helper()
	c.Literals.IndexLiteralCandidates(t.Context(), reader, query, entries)
	reader.store.mu.Lock()
	build := reader.store.content[indexLiteralScopeKey(query)]
	reader.store.mu.Unlock()
	select {
	case <-build.done:
	case <-t.Context().Done():
		t.Fatal("content preparation did not drain")
	}
	return c.Literals.IndexLiteralCandidates(t.Context(), reader, query, entries)
}

func TestContentProofsRefreshWithinOneMetadataGeneration(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeLiteralFixture(t, root.Path, "file.txt", "\x00xxxxxxx")
	reader := readyIndex(t, c, root)
	entries, err := reader.FilePage(t.Context(), FileScope{Audience: HumanAudience}, "", TreeFilePageLimit)
	testutil.FailErr(t, "metadata page", err)
	var generation int64 = 1
	c.Literals.cache.versionOf = func(root string, entry Entry) fspath.FileObservation {
		version := fixtureFileVersion(root, entry)
		version.Changed = generation
		return version
	}
	var opens atomic.Int32
	query := LiteralQuery{IncludeKey: "lifecycle", Require: litprefilter.AnyOf("needle"), Open: func(entry Entry) (io.ReadCloser, error) { opens.Add(1); return literalTestOpener(root.Path)(entry) }}
	revision := reader.Status.Revision
	first := preparedLiteralPage(t, c, reader, query, entries)
	if len(first.Candidates) != 0 {
		t.Fatal("known binary was searched as decoded text")
	}
	info, err := os.Stat(filepath.Join(root.Path, "file.txt"))
	testutil.FailErr(t, "original metadata", err)
	writeLiteralFixture(t, root.Path, "file.txt", "needle")
	testutil.FailErr(t, "restore mtime", os.Chtimes(filepath.Join(root.Path, "file.txt"), info.ModTime(), info.ModTime()))
	generation++
	current := c.Literals.IndexLiteralCandidates(t.Context(), reader, query, entries)
	if len(current.Candidates) != 1 {
		t.Fatal("a stale binary proof hid live text")
	}
	warm := preparedLiteralPage(t, c, reader, query, entries)
	if warm.Warming || len(warm.Candidates) != 1 || opens.Load() != 2 {
		t.Fatalf("warm=%+v opens=%d", warm, opens.Load())
	}
	if reader.Status.Revision != revision {
		t.Fatalf("test crossed metadata generations: %d", reader.Status.Revision)
	}
}

func TestContentPreparationReusesFactsAcrossMetadataGenerations(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeLiteralFixture(t, root.Path, "a.txt", "ordinary text")
	first := readyIndex(t, c, root)
	entries, err := first.FilePage(t.Context(), FileScope{Audience: HumanAudience}, "", TreeFilePageLimit)
	testutil.FailErr(t, "first page", err)
	var opens atomic.Int32
	query := LiteralQuery{IncludeKey: "reuse", Require: litprefilter.AnyOf("absent-token"), Open: func(entry Entry) (io.ReadCloser, error) { opens.Add(1); return literalTestOpener(root.Path)(entry) }}
	preparedLiteralPage(t, c, first, query, entries)
	testutil.FailErr(t, "release first metadata", first.Close())
	writeLiteralFixture(t, root.Path, "b.txt", "more ordinary text")
	c.InvalidateRoot(root.Path, "b.txt")
	next := readyIndex(t, c, root)
	entries, err = next.FilePage(t.Context(), FileScope{Audience: HumanAudience}, "", TreeFilePageLimit)
	testutil.FailErr(t, "next page", err)
	warm := preparedLiteralPage(t, c, next, query, entries)
	if warm.Warming || len(warm.Candidates) != 0 || opens.Load() != 2 {
		t.Fatalf("opens=%d warm=%+v", opens.Load(), warm)
	}
}

func TestCapacityEvictionDoesNotRestartCompletedPreparation(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeLiteralFixture(t, root.Path, "a.txt", "ordinary text")
	reader := readyIndex(t, c, root)
	entries, err := reader.FilePage(t.Context(), FileScope{Audience: HumanAudience}, "", TreeFilePageLimit)
	testutil.FailErr(t, "metadata page", err)
	var opens atomic.Int32
	query := LiteralQuery{IncludeKey: "capacity", Require: litprefilter.AnyOf("absent-token"), Open: func(entry Entry) (io.ReadCloser, error) { opens.Add(1); return literalTestOpener(root.Path)(entry) }}
	preparedLiteralPage(t, c, reader, query, entries)
	reader.store.mu.Lock()
	build := reader.store.content[indexLiteralScopeKey(query)]
	reader.store.mu.Unlock()
	c.Literals.cache.fileMu.Lock()
	c.Literals.cache.files.evict(c.Literals.cache.files.order[0])
	c.Literals.cache.fileMu.Unlock()
	for range 5 {
		page := c.Literals.IndexLiteralCandidates(t.Context(), reader, query, entries)
		if page.Warming || len(page.Candidates) != 1 {
			t.Fatalf("eviction changed coverage: %+v", page)
		}
	}
	reader.store.mu.Lock()
	current := reader.store.content[indexLiteralScopeKey(query)]
	reader.store.mu.Unlock()
	if current != build || opens.Load() != 1 {
		t.Fatalf("eviction restarted preparation: opens=%d", opens.Load())
	}
}

func TestUnprovableContentFinishesPreparationWithoutRetryLoops(t *testing.T) {
	for _, version := range []fspath.FileObservation{{}, {Known: true, Links: 0, Size: 7}} {
		c := treeTestCatalog(t)
		root := Root{ID: "root", Path: t.TempDir()}
		writeLiteralFixture(t, root.Path, "a.txt", "content")
		reader := readyIndex(t, c, root)
		entries, err := reader.FilePage(t.Context(), FileScope{Audience: HumanAudience}, "", TreeFilePageLimit)
		testutil.FailErr(t, "metadata page", err)
		c.Literals.cache.versionOf = func(string, Entry) fspath.FileObservation { return version }
		var opens atomic.Int32
		query := LiteralQuery{IncludeKey: "unprovable", Require: litprefilter.AnyOf("absent-token"), Open: func(Entry) (io.ReadCloser, error) {
			opens.Add(1)
			return io.NopCloser(strings.NewReader("content")), nil
		}}
		preparedLiteralPage(t, c, reader, query, entries)
		for range 5 {
			page := c.Literals.IndexLiteralCandidates(t.Context(), reader, query, entries)
			if page.Warming || len(page.Candidates) != 1 {
				t.Fatalf("unprovable file lost live fallback: %+v", page)
			}
		}
		if opens.Load() != 0 {
			t.Fatalf("unprovable file was prepared %d times", opens.Load())
		}
	}
}

func TestReportedZeroSizeCannotPublishNonemptyContent(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeLiteralFixture(t, root.Path, "dynamic.txt", "")
	reader := readyIndex(t, c, root)
	entries, err := reader.FilePage(t.Context(), FileScope{Audience: HumanAudience}, "", TreeFilePageLimit)
	testutil.FailErr(t, "metadata page", err)
	var opens atomic.Int32
	query := LiteralQuery{IncludeKey: "dynamic", Require: litprefilter.AnyOf("needle"), Open: func(Entry) (io.ReadCloser, error) {
		opens.Add(1)
		return io.NopCloser(strings.NewReader("needle")), nil
	}}
	preparedLiteralPage(t, c, reader, query, entries)
	for range 5 {
		page := c.Literals.IndexLiteralCandidates(t.Context(), reader, query, entries)
		if page.Warming || len(page.Candidates) != 1 {
			t.Fatalf("partial observation became proof: %+v", page)
		}
	}
	if opens.Load() != 0 || !zeroSizeMarkerOnly(c.Literals.cache, root.Path, "dynamic.txt") {
		t.Fatalf("opens=%d retained=%d", opens.Load(), c.Literals.cache.files.count)
	}
	testutil.FailErr(t, "drain incomplete observation", c.Drain(context.Background()))
}

func TestChangedProofBecomesUnprovableWithoutRepeatedPreparation(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeLiteralFixture(t, root.Path, "a.txt", "content")
	reader := readyIndex(t, c, root)
	entries, err := reader.FilePage(t.Context(), FileScope{Audience: HumanAudience}, "", TreeFilePageLimit)
	testutil.FailErr(t, "metadata page", err)
	known := true
	c.Literals.cache.versionOf = func(root string, entry Entry) fspath.FileObservation {
		version := fixtureFileVersion(root, entry)
		version.Known = known
		return version
	}
	var opens atomic.Int32
	query := LiteralQuery{IncludeKey: "links", Require: litprefilter.AnyOf("absent-token"), Open: func(entry Entry) (io.ReadCloser, error) { opens.Add(1); return literalTestOpener(root.Path)(entry) }}
	preparedLiteralPage(t, c, reader, query, entries)
	known = false
	page := preparedLiteralPage(t, c, reader, query, entries)
	if len(page.Candidates) != 1 || page.Warming {
		t.Fatalf("link alias lost fallback: %+v", page)
	}
	reader.store.mu.Lock()
	build := reader.store.content[indexLiteralScopeKey(query)]
	reader.store.mu.Unlock()
	for range 5 {
		page = c.Literals.IndexLiteralCandidates(t.Context(), reader, query, entries)
		if page.Warming || len(page.Candidates) != 1 {
			t.Fatalf("unprovable file retried: %+v", page)
		}
	}
	reader.store.mu.Lock()
	sameBuild := reader.store.content[indexLiteralScopeKey(query)] == build
	reader.store.mu.Unlock()
	if !sameBuild || opens.Load() != 1 {
		t.Fatalf("permanent gap restarted preparation: opens=%d", opens.Load())
	}
	known = true
	page = preparedLiteralPage(t, c, reader, query, entries)
	if page.Warming || len(page.Candidates) != 0 || opens.Load() != 2 {
		t.Fatalf("proof did not recover: opens=%d page=%+v", opens.Load(), page)
	}
}

func TestRetainedMetadataCannotRestartPreparationAfterDrain(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeLiteralFixture(t, root.Path, "a.txt", "content")
	reader := readyIndex(t, c, root)
	entries, err := reader.FilePage(t.Context(), FileScope{Audience: HumanAudience}, "", TreeFilePageLimit)
	testutil.FailErr(t, "metadata page", err)
	var opens atomic.Int32
	query := LiteralQuery{IncludeKey: "drained", Require: litprefilter.AnyOf("needle"), Open: func(entry Entry) (io.ReadCloser, error) { opens.Add(1); return literalTestOpener(root.Path)(entry) }}
	testutil.FailErr(t, "drain catalog", c.Drain(t.Context()))
	for range 5 {
		page := c.Literals.IndexLiteralCandidates(t.Context(), reader, query, entries)
		if page.Warming {
			t.Fatal("drained metadata restarted content preparation")
		}
	}
	if opens.Load() != 0 {
		t.Fatalf("drained catalog opened %d files", opens.Load())
	}
}

func TestZeroSizeSourceAlwaysKeepsLiveCandidates(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeLiteralFixture(t, root.Path, "dynamic.txt", "")
	reader := readyIndex(t, c, root)
	entries, err := reader.FilePage(t.Context(), FileScope{Audience: HumanAudience}, "", TreeFilePageLimit)
	testutil.FailErr(t, "metadata page", err)
	content := ""
	var opens atomic.Int32
	query := LiteralQuery{IncludeKey: "variable-zero-size", Require: litprefilter.AnyOf("needle"), Open: func(Entry) (io.ReadCloser, error) { opens.Add(1); return io.NopCloser(strings.NewReader(content)), nil }}
	for _, next := range []string{"", "needle", "", "more needle text"} {
		content = next
		page := preparedLiteralPage(t, c, reader, query, entries)
		if page.Warming || len(page.Candidates) != 1 || !zeroSizeMarkerOnly(c.Literals.cache, root.Path, "dynamic.txt") {
			t.Fatalf("zero-size content was excluded: %+v", page)
		}
	}
	if opens.Load() != 0 {
		t.Fatalf("zero-size preparation opened %d inputs", opens.Load())
	}
}

func zeroSizeMarkerOnly(cache *literalIndexCache, root, path string) bool {
	cache.fileMu.Lock()
	defer cache.fileMu.Unlock()
	fact, found := cache.files.lookup(root, path)
	return found && fact.unavailable && fact.version.Size == 0 && len(fact.bloom) == 0 && len(fact.rawBloom) == 0
}

func TestZeroSizeSourceRecoversPruningWithinPinnedMetadata(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeLiteralFixture(t, root.Path, "file.txt", "")
	reader := readyIndex(t, c, root)
	entries, err := reader.FilePage(t.Context(), FileScope{Audience: HumanAudience}, "", TreeFilePageLimit)
	testutil.FailErr(t, "metadata page", err)
	initialRevision := reader.Status.Revision
	var opens atomic.Int32
	query := LiteralQuery{IncludeKey: "zero-size-recovery", Require: litprefilter.AnyOf("absent-token"), Open: func(entry Entry) (io.ReadCloser, error) { opens.Add(1); return literalTestOpener(root.Path)(entry) }}
	first := preparedLiteralPage(t, c, reader, query, entries)
	if len(first.Candidates) != 1 || !zeroSizeMarkerOnly(c.Literals.cache, root.Path, "file.txt") {
		t.Fatal("zero-size address lost its live fallback")
	}
	writeLiteralFixture(t, root.Path, "file.txt", "ordinary content")
	warm := preparedLiteralPage(t, c, reader, query, entries)
	if warm.Warming || len(warm.Candidates) != 0 || opens.Load() != 1 || reader.Status.Revision != initialRevision {
		t.Fatalf("recovery changed generation or failed pruning: opens=%d page=%+v", opens.Load(), warm)
	}
	for range 5 {
		page := c.Literals.IndexLiteralCandidates(t.Context(), reader, query, entries)
		if page.Warming || len(page.Candidates) != 0 {
			t.Fatalf("recovered facts were not reused: %+v", page)
		}
	}
	if opens.Load() != 1 {
		t.Fatalf("recovered proof reopened %d times", opens.Load())
	}
}
