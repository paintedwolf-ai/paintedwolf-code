package sourcetree

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestClosedDirectoryDiscoveryPreservesVisibleRevision(t *testing.T) {
	view, root := viewFixture(t)
	for _, dir := range []string{"visible", "closed"} {
		testutil.FailErr(t, "create directory", os.Mkdir(filepath.Join(root.Path, dir), 0700))
		testutil.FailErr(t, "create file", os.WriteFile(filepath.Join(root.Path, dir, "file.txt"), []byte("source"), 0600))
	}
	<-view.Prepare()
	testutil.FailErr(t, "open visible directory", view.Disclose(t.Context(), nil, IntentEntry{Address: Address{Root: root.ID, Path: "visible"}, Disclosure: Disclosure{Open: true, Recursive: false}}))
	<-view.Prepare()
	before, extent, err := view.Revision(t.Context())
	testutil.FailErr(t, "read initial revision", err)
	_, err = view.catalog.Directories.ObserveDirectory(t.Context(), view.scope.Project, root, "closed", sourcecatalog.DirectoryRead{Priority: backgroundwork.PriorityProactive})
	testutil.FailErr(t, "discover closed directory", err)
	after, current, err := view.Revision(t.Context())
	testutil.FailErr(t, "read unchanged presentation", err)
	if before != after || extent != current {
		t.Fatalf("closed discovery invalidated presentation: %v %v -> %v %v", before, extent, after, current)
	}
	testutil.FailErr(t, "add visible child", os.WriteFile(filepath.Join(root.Path, "visible", "next.txt"), []byte("next"), 0600))
	view.catalog.InvalidateRoot(root.Path, "visible/next.txt")
	_, err = view.catalog.Directories.ObserveDirectory(t.Context(), view.scope.Project, root, "visible", sourcecatalog.DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "refresh open directory", err)
	after, current, err = view.Revision(t.Context())
	testutil.FailErr(t, "read changed presentation", err)
	if before.Projection == after.Projection || current.Rows != extent.Rows+1 {
		t.Fatalf("visible update missed: %v %v -> %v %v", before, extent, after, current)
	}
}

func TestRecursiveRevisionIgnoresClosedExceptionsAndUnchangedRefresh(t *testing.T) {
	view, root := viewFixture(t)
	for _, dir := range []string{"shown", "hidden"} {
		testutil.FailErr(t, "create directory", os.Mkdir(filepath.Join(root.Path, dir), 0700))
		testutil.FailErr(t, "create child", os.WriteFile(filepath.Join(root.Path, dir, "file.txt"), []byte("source"), 0600))
	}
	observeFixture(t, view, root)
	testutil.FailErr(t, "close exception", view.Disclose(t.Context(), nil, IntentEntry{Address: Address{Root: root.ID, Path: "hidden"}, Disclosure: Disclosure{Open: false, Recursive: false}}))
	before, _, err := view.Revision(t.Context())
	testutil.FailErr(t, "read recursive revision", err)
	testutil.FailErr(t, "create hidden child", os.WriteFile(filepath.Join(root.Path, "hidden", "new.txt"), []byte("new"), 0600))
	view.catalog.InvalidateRoot(root.Path, "hidden/new.txt")
	_, err = view.catalog.Directories.ObserveDirectory(t.Context(), view.scope.Project, root, "hidden", sourcecatalog.DirectoryRead{Priority: backgroundwork.PriorityProactive})
	testutil.FailErr(t, "observe hidden mutation", err)
	after, _, err := view.Revision(t.Context())
	testutil.FailErr(t, "read unchanged recursive projection", err)
	if before != after {
		t.Fatalf("closed exception invalidated rows: %v -> %v", before, after)
	}
	view.catalog.InvalidateRoot(root.Path, "shown/file.txt")
	_, err = view.catalog.Directories.ObserveDirectory(t.Context(), view.scope.Project, root, "shown", sourcecatalog.DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "refresh unchanged listing", err)
	after, _, err = view.Revision(t.Context())
	testutil.FailErr(t, "read unchanged refreshed projection", err)
	if before != after {
		t.Fatalf("unchanged listing invalidated rows: %v -> %v", before, after)
	}
}
