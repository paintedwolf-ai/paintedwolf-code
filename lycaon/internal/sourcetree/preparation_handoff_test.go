package sourcetree

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCaptureKeepsCompleteCoordinatesWhileNewMembershipIsUnresolved(t *testing.T) {
	view, root := viewFixture(t)
	testutil.FailErr(t, "write known file", os.WriteFile(filepath.Join(root.Path, "known.txt"), []byte("known"), 0600))
	_, err := view.catalog.Directories.ObserveDirectory(t.Context(), "project", root, ".", sourcecatalog.DirectoryRead{})
	testutil.FailErr(t, "publish complete initial structure", err)
	view.mu.Lock()
	view.rules.Set(Address{Root: root.ID, Path: "."}, Disclosure{Open: true, Recursive: true})
	view.mu.Unlock()
	testutil.FailErr(t, "create new directory", os.Mkdir(filepath.Join(root.Path, "new"), 0700))
	testutil.FailErr(t, "write new descendant", os.WriteFile(filepath.Join(root.Path, "new", "child.txt"), []byte("new"), 0600))
	repochange.Advance(root.Path)
	_, err = view.catalog.Directories.ObserveDirectory(t.Context(), "project", root, ".", sourcecatalog.DirectoryRead{})
	testutil.FailErr(t, "publish foreground membership with unknown child", err)
	presentation, err := view.Capture(t.Context())
	testutil.FailErr(t, "capture completed predecessor immediately", err)
	defer presentation.Close()
	_, extent := presentation.Revision()
	if !extent.Complete || extent.Rows != 2 {
		t.Fatalf("retained coordinates=%+v", extent)
	}
	testutil.FailErr(t, "finish queued recursive update", view.catalog.Directories.AwaitNavigation(t.Context(), "project", root))
	refreshed, err := view.Capture(t.Context())
	testutil.FailErr(t, "capture newly complete head", err)
	defer refreshed.Close()
	_, extent = refreshed.Revision()
	if !extent.Complete || extent.Rows != 4 {
		t.Fatalf("refreshed coordinates=%+v", extent)
	}
	frame, err := presentation.Frame(t.Context(), FrameRequest{Offset: 0, Limit: 10})
	testutil.FailErr(t, "read retained presentation after completion replacement", err)
	if len(frame.Rows) != 2 || frame.Rows[1].Address.Path != "known.txt" {
		t.Fatalf("retained rows=%+v", frame.Rows)
	}
}

func TestExplicitNewDirectoryUsesCurrentIncompleteMembership(t *testing.T) {
	view, root := viewFixture(t)
	_, err := view.catalog.Directories.ObserveDirectory(t.Context(), "project", root, ".", sourcecatalog.DirectoryRead{})
	testutil.FailErr(t, "publish complete initial structure", err)
	testutil.FailErr(t, "create new directory", os.Mkdir(filepath.Join(root.Path, "new"), 0700))
	repochange.Advance(root.Path)
	_, err = view.catalog.Directories.ObserveDirectory(t.Context(), "project", root, ".", sourcecatalog.DirectoryRead{})
	testutil.FailErr(t, "publish new incomplete membership", err)
	view.mu.Lock()
	view.rules.Set(Address{Root: root.ID, Path: "new"}, Disclosure{Open: true})
	view.mu.Unlock()

	demands, _, err := view.coverageDemand(t.Context())
	testutil.FailErr(t, "resolve explicit directory demand", err)
	if len(demands) != 1 || demands[0].path != "new" || demands[0].recursive {
		t.Fatalf("explicit directory demand=%+v", demands)
	}
}
