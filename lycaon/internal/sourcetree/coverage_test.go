package sourcetree

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestExtentCoverageFollowsMixedDisclosures(t *testing.T) {
	view, root := viewFixture(t)
	testutil.FailErr(t, "create pending subtree", os.MkdirAll(filepath.Join(root.Path, "a", "nested"), 0700))
	testutil.FailErr(t, "create another subtree", os.Mkdir(filepath.Join(root.Path, "b"), 0700))
	observe := func(dir string) {
		_, err := view.catalog.ObserveDirectory(t.Context(), "project", root, dir, sourcecatalog.DirectoryRead{Priority: backgroundwork.PriorityInteractive})
		testutil.FailErr(t, "observe selected directory", err)
	}
	check := func(complete bool) {
		t.Helper()
		frame, err := view.snapshot(t.Context())
		testutil.FailErr(t, "read extent coverage", err)
		defer frame.Close()
		if frame.complete != complete {
			t.Fatalf("complete=%v, want %v", frame.complete, complete)
		}
	}
	// Set intent directly so only explicit observations advance this fixture.
	set := func(dir string, open, recursive bool) {
		view.mu.Lock()
		view.rules.Set(Address{Root: root.ID, Path: dir}, Disclosure{Open: open, Recursive: recursive})
		view.mu.Unlock()
	}
	observe(".")
	check(true)
	set(".", true, true)
	check(false)
	observe("a")
	observe("b")
	check(false)
	set("a/nested", false, false)
	check(true)
	set("a/nested", true, false)
	check(false)
	observe("a/nested")
	check(true)
}
