package sourcecatalog

import (
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestNavigationSubscribersFollowCommittedRootPublications(t *testing.T) {
	catalog, root := indexFixture(t)
	var matching, unrelated atomic.Int32
	stop := catalog.Directories.SubscribeNavigation(root, func() { matching.Add(1) })
	defer stop()
	other := catalog.Directories.SubscribeNavigation(Root{ID: "other", Path: t.TempDir()}, func() { unrelated.Add(1) })
	defer other()
	writeIndexFile(t, root.Path, "file.txt", "source")
	_, err := catalog.Directories.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "observe subscribed root", err)
	if matching.Load() == 0 || unrelated.Load() != 0 {
		t.Fatalf("matching=%d unrelated=%d", matching.Load(), unrelated.Load())
	}
	before := matching.Load()
	_, err = catalog.Directories.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "reuse fresh observation", err)
	if matching.Load() != before {
		t.Fatal("cache read emitted a false publication")
	}
	stop()
	catalog.Directories.navigationChanged(root)
	if matching.Load() != before {
		t.Fatal("released subscriber received a publication")
	}
}
