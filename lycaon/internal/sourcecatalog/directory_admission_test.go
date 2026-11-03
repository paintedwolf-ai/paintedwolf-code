package sourcecatalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type testDirectoryAdmission struct {
	file  *os.File
	admit func() error
}

func (d *testDirectoryAdmission) DirectoryFile() *os.File { return d.file }
func (d *testDirectoryAdmission) AdmitDirectoryRead() error {
	if d.admit != nil {
		return d.admit()
	}
	return nil
}

func TestDirectoryReaderReadmitsBorrowedMembershipBetweenBatches(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "dir/known", "source")
	initial := catalog.Directories.NewDirectoryReader("p", root)
	_, err := initial.ReadDirectory(t.Context(), "dir", 0, nil)
	testutil.FailErr(t, "capture known membership", err)
	testutil.FailErr(t, "publish known membership", initial.Close(t.Context()))
	for i := range indexBatchSize + 2 {
		writeIndexFile(t, root.Path, fmt.Sprintf("dir/new-%04d", i), "source")
	}
	file, err := os.Open(filepath.Join(root.Path, "dir"))
	testutil.FailErr(t, "open borrowed directory", err)
	defer file.Close()
	denied := errors.New("directory read denied")
	calls := 0
	capability := &testDirectoryAdmission{file: file, admit: func() error {
		calls++
		if calls == 3 {
			return denied
		}
		return nil
	}}
	reader := catalog.Directories.NewDirectoryReader("p", root)
	listing, err := reader.ReadDirectory(t.Context(), "dir", 0, capability)
	if !errors.Is(err, denied) || calls != 3 || len(listing.Entries) != 0 {
		t.Fatalf("admission calls=%d listing=%+v error=%v", calls, listing, err)
	}
	if reader.builder != nil || reader.pin != nil {
		t.Fatal("denied batch retained unpublished resources")
	}
	testutil.FailErr(t, "close denied reader", reader.Close(t.Context()))
	observation, err := reader.store.readObservation(t.Context(), "dir")
	testutil.FailErr(t, "read denied observation", err)
	if observation.Complete || observation.Failure == "" || observation.Entries != 1 {
		t.Fatalf("denied observation=%+v", observation)
	}
	navigation, err := openNavigation(t.Context(), reader.store, headGeneration)
	testutil.FailErr(t, "open preserved namespace", err)
	defer navigation.Close()
	children, err := navigation.Children(t.Context(), "dir")
	testutil.FailErr(t, "read preserved namespace", err)
	count, err := children.Count(t.Context())
	testutil.FailErr(t, "count preserved children", err)
	if count != 1 {
		t.Fatalf("denied batch published new children: count=%d", count)
	}
	child, _, err := navigation.Child(t.Context(), "dir", 0, false)
	testutil.FailErr(t, "read preserved child", err)
	if child.Name != "known" {
		t.Fatalf("denied batch replaced known child: %+v", child)
	}
}

func TestDirectoryReaderReadmitsReusedBorrowedNamespace(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "dir/known", "source")
	initial := catalog.Directories.NewDirectoryReader("p", root)
	_, err := initial.ReadDirectory(t.Context(), "dir", 0, nil)
	testutil.FailErr(t, "capture reusable membership", err)
	testutil.FailErr(t, "publish reusable membership", initial.Close(t.Context()))
	file, err := os.Open(filepath.Join(root.Path, "dir"))
	testutil.FailErr(t, "open reusable directory", err)
	defer file.Close()
	denied := errors.New("reused directory denied")
	capability := &testDirectoryAdmission{file: file, admit: func() error { return denied }}
	reader := catalog.Directories.NewDirectoryReader("p", root)
	listing, err := reader.ReadDirectory(t.Context(), "dir", 0, capability)
	if !errors.Is(err, denied) || len(listing.Entries) != 0 {
		t.Fatalf("reused admission listing=%+v error=%v", listing, err)
	}
	testutil.FailErr(t, "close denied reused reader", reader.Close(t.Context()))
}
