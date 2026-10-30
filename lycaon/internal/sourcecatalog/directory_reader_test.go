package sourcecatalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

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
	listing, err := reader.ReadDirectory(t.Context(), "dir", 0, &testDirectoryAdmission{file: held})
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

func TestDirectoryReaderCannotClaimOmittedLink(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "kept", "source")
	testutil.FailErr(t, "create omitted link", os.Symlink("missing", filepath.Join(root.Path, "dangling")))
	reader := catalog.Directories.NewDirectoryReader("p", root)
	listing, err := reader.ReadDirectory(t.Context(), ".", 0, nil)
	if !errors.Is(err, ErrDirectoryNamespace) || len(listing.Entries) != 0 {
		t.Fatalf("omitted namespace=%+v error=%v", listing, err)
	}
	testutil.FailErr(t, "close omitted namespace reader", reader.Close(t.Context()))
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

func TestDirectoryReaderRejectsReplacedAttachedRoot(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "dir/old", "source")
	pending := catalog.Directories.NewDirectoryReader("p", root)
	_, err := pending.ReadDirectory(t.Context(), "dir", 0, nil)
	testutil.FailErr(t, "collect original attached root", err)
	moved := filepath.Join(t.TempDir(), "moved-root")
	testutil.FailErr(t, "move attached root", os.Rename(root.Path, moved))
	writeIndexFile(t, root.Path, "dir/new", "source")
	if err := pending.Close(t.Context()); !errors.Is(err, errObservationChanged) {
		t.Fatalf("replaced-root drain=%v", err)
	}
	current := catalog.Directories.NewDirectoryReader("p", root)
	if _, err := current.ReadDirectory(t.Context(), "dir", 0, nil); !errors.Is(err, errObservationChanged) {
		t.Fatalf("cached descriptor read replaced root: %v", err)
	}
	testutil.FailErr(t, "close replaced-root reader", current.Close(t.Context()))
	if _, err := pending.store.readObservation(t.Context(), "dir"); !errors.Is(err, pagedview.ErrMissing) {
		t.Fatalf("published replaced-root observation: %v", err)
	}
}

func TestDirectoryNamespaceReuseRejectsReplacedRootCapability(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "dir/old", "source")
	reader := catalog.Directories.NewDirectoryReader("p", root)
	_, err := reader.ReadDirectory(t.Context(), "dir", 0, nil)
	testutil.FailErr(t, "collect reusable namespace", err)
	testutil.FailErr(t, "publish reusable namespace", reader.Close(t.Context()))
	observation, err := reader.store.readObservation(t.Context(), "dir")
	testutil.FailErr(t, "read complete observation", err)
	testutil.FailErr(t, "move cached root", os.Rename(root.Path, filepath.Join(t.TempDir(), "moved-root")))
	writeIndexFile(t, root.Path, "dir/new", "source")
	if _, err := readDirectoryNamespace(t.Context(), reader.store, "dir", observation, 0, true); !errors.Is(err, errObservationChanged) {
		t.Fatalf("reused stale root capability: %v", err)
	}
}
