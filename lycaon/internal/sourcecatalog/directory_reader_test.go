package sourcecatalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

func readerGeneration(t *testing.T, store *indexStore) int64 {
	t.Helper()
	pin, err := store.retainGeneration(headGeneration, true)
	testutil.FailErr(t, "retain reader generation", err)
	defer pin.Release()
	return pin.Generation
}

func TestDirectoryReaderBatchesPublicationAndReadsFreshNamesDirectly(t *testing.T) {
	catalog, root := indexFixture(t)
	for i := range foregroundDirectoryBatch + 1 {
		writeIndexFile(t, root.Path, fmt.Sprintf("dir-%03d/value", i), "source")
	}
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "open reader store", err)
	before := readerGeneration(t, store)
	reader := catalog.Directories.NewDirectoryReader("p", root)
	for i := range foregroundDirectoryBatch + 1 {
		listing, err := reader.ReadDirectory(t.Context(), fmt.Sprintf("dir-%03d", i), 0, nil)
		testutil.FailErr(t, "read foreground names", err)
		if !listing.Prepared || listing.Reused || len(listing.Entries) != 1 || listing.Entries[0].Name != "value" {
			t.Fatalf("listing %d=%+v", i, listing)
		}
		want := before + int64((i+1)/foregroundDirectoryBatch)
		if got := readerGeneration(t, store); got != want {
			t.Fatalf("after directory %d generation=%d want=%d", i, got, want)
		}
		if len(reader.pending) >= foregroundDirectoryBatch || reader.entries >= foregroundEntryBatch {
			t.Fatalf("batch exceeded bound: directories=%d entries=%d", len(reader.pending), reader.entries)
		}
	}
	if _, err := store.readObservation(t.Context(), "dir-064"); !errors.Is(err, pagedview.ErrMissing) {
		t.Fatalf("fresh names required publication: %v", err)
	}
	testutil.FailErr(t, "drain foreground observations", reader.Close(t.Context()))
	if got := readerGeneration(t, store); got != before+2 {
		t.Fatalf("drained generations=%d want=%d", got, before+2)
	}
	testutil.FailErr(t, "close reader twice", reader.Close(t.Context()))
	if _, err := reader.ReadDirectory(t.Context(), ".", 0, nil); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed reader=%v", err)
	}
	after := catalog.Directories.NewDirectoryReader("p", root)
	reused, err := after.ReadDirectory(t.Context(), "dir-064", 0, nil)
	testutil.FailErr(t, "close reused reader", after.Close(t.Context()))
	testutil.FailErr(t, "reuse drained membership", err)
	if !reused.Reused {
		t.Fatalf("drained namespace=%+v", reused)
	}
}

func TestDirectoryReaderPartialCancellationAndFailure(t *testing.T) {
	catalog, root := indexFixture(t)
	for _, name := range []string{"a", "b", "c"} {
		writeIndexFile(t, root.Path, "bounded/"+name, "source")
	}
	reader := catalog.Directories.NewDirectoryReader("p", root)
	limited, err := reader.ReadDirectory(t.Context(), "bounded", 1, nil)
	if !errors.Is(err, ErrDirectoryNamespace) || !limited.Prepared || len(limited.Entries) != 0 {
		t.Fatalf("limited=%+v error=%v", limited, err)
	}
	observation, err := reader.store.readObservation(t.Context(), "bounded")
	testutil.FailErr(t, "read partial observation", err)
	if observation.Complete || observation.Entries != 2 {
		t.Fatalf("partial=%+v", observation)
	}
	complete, err := reader.ReadDirectory(t.Context(), "bounded", 3, nil)
	testutil.FailErr(t, "complete partial directory", err)
	if len(complete.Entries) != 3 {
		t.Fatalf("complete=%+v", complete)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := reader.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled drain=%v", err)
	}
	if reader.builder != nil || reader.pin != nil || len(reader.pending) != 0 {
		t.Fatal("canceled reader retained batch resources")
	}
	observation, err = reader.store.readObservation(t.Context(), "bounded")
	testutil.FailErr(t, "read canceled partial observation", err)
	if observation.Complete {
		t.Fatal("canceled drain published complete coverage")
	}
	failed := catalog.Directories.NewDirectoryReader("p", root)
	if _, err := failed.ReadDirectory(t.Context(), "missing", 0, nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing namespace=%v", err)
	}
	observation, err = failed.store.readObservation(t.Context(), "missing")
	testutil.FailErr(t, "read failed observation", err)
	if observation.Complete || observation.Failure == "" {
		t.Fatalf("failure=%+v", observation)
	}
	testutil.FailErr(t, "close failed reader", failed.Close(t.Context()))
}

func TestDirectoryReaderRevalidatesChangesAndPreservesByteOrder(t *testing.T) {
	catalog, root := indexFixture(t)
	for _, name := range []string{"Z", "a", ".hidden"} {
		writeIndexFile(t, root.Path, "dir/"+name, "source")
	}
	first := catalog.Directories.NewDirectoryReader("p", root)
	listing, err := first.ReadDirectory(t.Context(), "dir", 0, nil)
	testutil.FailErr(t, "read ordered fresh listing", err)
	var names []string
	for _, entry := range listing.Entries {
		names = append(names, entry.Name)
	}
	if !slices.Equal(names, []string{".hidden", "Z", "a"}) {
		t.Fatalf("byte order=%v", names)
	}
	testutil.FailErr(t, "publish initial names", first.Close(t.Context()))
	writeIndexFile(t, root.Path, "dir/new", "source")
	second := catalog.Directories.NewDirectoryReader("p", root)
	listing, err = second.ReadDirectory(t.Context(), "dir", 0, nil)
	testutil.FailErr(t, "read changed listing", err)
	if listing.Reused || len(listing.Entries) != 4 {
		t.Fatalf("changed=%+v", listing)
	}
	testutil.FailErr(t, "publish changed names", second.Close(t.Context()))
	store := second.store
	store.mu.Lock()
	store.invalidateObservationsLocked([]string{"dir/new"})
	store.mu.Unlock()
	third := catalog.Directories.NewDirectoryReader("p", root)
	listing, err = third.ReadDirectory(t.Context(), "dir", 0, nil)
	testutil.FailErr(t, "read invalidated listing", err)
	if !listing.Prepared {
		t.Fatalf("invalidation reused membership: %+v", listing)
	}
	testutil.FailErr(t, "drain invalidated listing", third.Close(t.Context()))
}

func TestDirectoryReaderBorrowedDescriptorDoesNotPublishRenamedNamespace(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "dir/kept", "source")
	held, err := os.Open(filepath.Join(root.Path, "dir"))
	testutil.FailErr(t, "open borrowed directory", err)
	defer func() { testutil.FailErr(t, "close borrowed directory", held.Close()) }()
	reader := catalog.Directories.NewDirectoryReader("p", root)
	listing, err := reader.ReadDirectory(t.Context(), "dir", 0, held)
	testutil.FailErr(t, "read borrowed directory", err)
	if len(listing.Entries) != 1 || listing.Entries[0].Name != "kept" {
		t.Fatalf("borrowed=%+v", listing)
	}
	testutil.FailErr(t, "move observed directory", os.Rename(filepath.Join(root.Path, "dir"), filepath.Join(root.Path, "moved")))
	writeIndexFile(t, root.Path, "dir/replacement", "source")
	if err := reader.Close(t.Context()); !errors.Is(err, errObservationChanged) {
		t.Fatalf("renamed drain=%v", err)
	}
	if _, err := held.Stat(); err != nil {
		t.Fatalf("catalog closed borrowed descriptor: %v", err)
	}
	if _, err := reader.store.readObservation(t.Context(), "dir"); !errors.Is(err, pagedview.ErrMissing) {
		t.Fatalf("published renamed membership: %v", err)
	}
}

func TestDirectoryReadersRebaseIndependentBatchesAndRespectInvalidation(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "a/first", "source")
	writeIndexFile(t, root.Path, "b/second", "source")
	left, right := catalog.Directories.NewDirectoryReader("p", root), catalog.Directories.NewDirectoryReader("p", root)
	_, err := left.ReadDirectory(t.Context(), "a", 0, nil)
	testutil.FailErr(t, "collect left namespace", err)
	_, err = right.ReadDirectory(t.Context(), "b", 0, nil)
	testutil.FailErr(t, "collect independent namespace", err)
	testutil.FailErr(t, "publish right batch", right.Close(t.Context()))
	writeIndexFile(t, root.Path, "a/new", "source")
	left.store.mu.Lock()
	left.store.invalidateObservationsLocked([]string{"a/new"})
	left.store.mu.Unlock()
	testutil.FailErr(t, "rebase left batch", left.Close(t.Context()))
	for _, dir := range []string{"a", "b"} {
		reader := catalog.Directories.NewDirectoryReader("p", root)
		listing, err := reader.ReadDirectory(t.Context(), dir, 0, nil)
		testutil.FailErr(t, "read rebased namespace", err)
		if dir == "a" && (listing.Reused || len(listing.Entries) != 2) {
			t.Fatalf("invalidated left=%+v", listing)
		}
		if dir == "b" && (!listing.Reused || len(listing.Entries) != 1) {
			t.Fatalf("lost right=%+v", listing)
		}
		testutil.FailErr(t, "close rebase reader", reader.Close(t.Context()))
	}
}

func TestDirectoryReaderEntryThresholdAndOmittedLink(t *testing.T) {
	catalog, root := indexFixture(t)
	for i := range foregroundEntryBatch {
		writeIndexFile(t, root.Path, fmt.Sprintf("large/file-%04d", i), "source")
	}
	reader := catalog.Directories.NewDirectoryReader("p", root)
	listing, err := reader.ReadDirectory(t.Context(), "large", 0, nil)
	testutil.FailErr(t, "read entry bound", err)
	if len(listing.Entries) != foregroundEntryBatch || reader.builder != nil || len(reader.pending) != 0 {
		t.Fatalf("entry threshold: entries=%d pending=%d builder=%t", len(listing.Entries), len(reader.pending), reader.builder != nil)
	}
	testutil.FailErr(t, "close entry threshold reader", reader.Close(t.Context()))
	testutil.FailErr(t, "create omitted link", os.Symlink("missing", filepath.Join(root.Path, "dangling")))
	omitted := catalog.Directories.NewDirectoryReader("p", root)
	listing, err = omitted.ReadDirectory(t.Context(), ".", 0, nil)
	if !errors.Is(err, ErrDirectoryNamespace) || len(listing.Entries) != 0 {
		t.Fatalf("omitted namespace=%+v error=%v", listing, err)
	}
	testutil.FailErr(t, "close omitted namespace reader", omitted.Close(t.Context()))
}

func TestDirectoryReaderRetirementReleasesUnpublishedResources(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "dir/value", "source")
	reader := catalog.Directories.NewDirectoryReader("p", root)
	_, err := reader.ReadDirectory(t.Context(), "dir", 0, nil)
	testutil.FailErr(t, "collect before retirement", err)
	testutil.FailErr(t, "retire catalog", catalog.Trees.Drain(t.Context()))
	if err := reader.Close(t.Context()); err == nil {
		t.Fatalf("retired drain=%v", err)
	}
	if reader.builder != nil || reader.pin != nil || len(reader.pending) != 0 {
		t.Fatal("retired reader retained pending resources")
	}
	reader.store.mu.Lock()
	defer reader.store.mu.Unlock()
	if reader.store.structure != nil || len(reader.store.pins.held) != 0 {
		t.Fatal("retired reader retained structural generation")
	}
}
