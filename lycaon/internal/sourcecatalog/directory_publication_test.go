package sourcecatalog

import (
	"errors"
	"fmt"
	"os"
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

func TestDirectoryReaderPublicationWorkScalesWithDirectoryBatches(t *testing.T) {
	for _, directories := range []int{1, 63, 64, 65, 128} {
		t.Run(fmt.Sprint(directories), func(t *testing.T) {
			verifyDirectoryPublicationWork(t, directories, 1, 64)
		})
	}
}

func TestDirectoryReaderPublicationWorkScalesWithEntryBatches(t *testing.T) {
	cases := []struct {
		name                         string
		directories, entries, period int
	}{
		{"before threshold", 1, 4095, 2},
		{"at threshold", 1, 4096, 1},
		{"past threshold", 1, 4097, 1},
		{"partial second batch", 5, 1024, 4},
		{"two batches", 8, 1024, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { verifyDirectoryPublicationWork(t, tc.directories, tc.entries, tc.period) })
	}
}

func verifyDirectoryPublicationWork(t *testing.T, directories, entries, period int) {
	t.Helper()
	catalog, root := indexFixture(t)
	for i := range directories {
		for j := range entries {
			writeIndexFile(t, root.Path, fmt.Sprintf("dir-%03d/value-%04d", i, j), "source")
		}
	}
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "open publication fixture", err)
	before := readerGeneration(t, store)
	reader := catalog.Directories.NewDirectoryReader("p", root)
	for i := range directories {
		listing, err := reader.ReadDirectory(t.Context(), fmt.Sprintf("dir-%03d", i), entries, nil)
		testutil.FailErr(t, "read fresh foreground membership", err)
		verifyDirectoryPublicationNames(t, listing, entries, false)
		if got, want := readerGeneration(t, store), before+int64((i+1)/period); got != want {
			t.Fatalf("directory %d: generation=%d want=%d", i, got, want)
		}
		if len(reader.pending) >= foregroundDirectoryBatch || reader.entries >= foregroundEntryBatch {
			t.Fatalf("retained batch exceeded thresholds: directories=%d entries=%d", len(reader.pending), reader.entries)
		}
	}
	if directories%period != 0 {
		if _, err := store.readObservation(t.Context(), fmt.Sprintf("dir-%03d", directories-1)); !errors.Is(err, pagedview.ErrMissing) {
			t.Fatalf("fresh listing required publication: %v", err)
		}
	}
	testutil.FailErr(t, "drain foreground membership", reader.Close(t.Context()))
	publications := int64((directories + period - 1) / period)
	if got := readerGeneration(t, store); got != before+publications {
		t.Fatalf("drained generation=%d want=%d", got, before+publications)
	}
	if reader.builder != nil || reader.pin != nil || len(reader.pending) != 0 || reader.entries != 0 {
		t.Fatal("closed reader retained batch resources")
	}
	testutil.FailErr(t, "close reader twice", reader.Close(t.Context()))
	if _, err := reader.ReadDirectory(t.Context(), ".", 0, nil); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed reader=%v", err)
	}
	warm := catalog.Directories.NewDirectoryReader("p", root)
	for i := range directories {
		listing, err := warm.ReadDirectory(t.Context(), fmt.Sprintf("dir-%03d", i), entries, nil)
		testutil.FailErr(t, "reuse drained membership", err)
		verifyDirectoryPublicationNames(t, listing, entries, true)
	}
	testutil.FailErr(t, "close warm reader", warm.Close(t.Context()))
	if got := readerGeneration(t, store); got != before+publications {
		t.Fatalf("warm membership republished: generation=%d want=%d", got, before+publications)
	}
}

func verifyDirectoryPublicationNames(t *testing.T, listing DirectoryListing, entries int, reused bool) {
	t.Helper()
	if listing.Reused != reused || listing.Prepared == reused || len(listing.Entries) != entries {
		t.Fatalf("membership reused=%t prepared=%t entries=%d want reused=%t entries=%d", listing.Reused, listing.Prepared, len(listing.Entries), reused, entries)
	}
	for i, entry := range listing.Entries {
		if entry.Name != fmt.Sprintf("value-%04d", i) || entry.IsDir || entry.IsSymlink {
			t.Fatalf("ordered entry %d=%+v", i, entry)
		}
	}
}
