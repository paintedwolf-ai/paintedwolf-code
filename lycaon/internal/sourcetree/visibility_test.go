package sourcetree

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRecursiveDisclosureKeepsGitAndIgnoredEntries(t *testing.T) {
	view, root := viewFixture(t)
	paths := []string{".git/objects/ab/entry", ".task/output.log", "ignored/file.txt", "nested/.git/HEAD"}
	for _, rel := range paths {
		testutil.FailErr(t, "create directory", os.MkdirAll(filepath.Dir(filepath.Join(root.Path, rel)), 0o755))
		testutil.FailErr(t, "create source", os.WriteFile(filepath.Join(root.Path, rel), []byte("source"), 0o644))
	}
	testutil.FailErr(t, "write ignore rules", os.WriteFile(filepath.Join(root.Path, ".gitignore"), []byte(".task/\nignored/\n"), 0o644))
	address := Address{Root: root.ID, Path: "."}
	testutil.FailErr(t, "expand all", view.Disclose(t.Context(), nil, IntentEntry{Address: address, Disclosure: Disclosure{Open: true, Recursive: true}}))
	testutil.FailErr(t, "await shared inventory", view.catalog.AwaitNavigation(t.Context(), view.scope.Project, root))
	<-view.Prepare()
	presentation := captureForTest(t, view)
	for _, rel := range paths {
		location, _, err := presentation.Locate(t.Context(), Address{Root: root.ID, Path: rel})
		testutil.FailErr(t, "locate human-visible path", err)
		if !location.Visible || location.Address.Path != rel {
			t.Fatalf("path %q is not visible: %+v", rel, location)
		}
		frame, err := presentation.Frame(t.Context(), FrameRequest{Offset: location.Index, Limit: 1})
		testutil.FailErr(t, "read located row", err)
		if len(frame.Rows) != 1 || frame.Rows[0].Address.Path != rel {
			t.Fatalf("located frame for %q: %+v", rel, frame)
		}
	}
	testutil.FailErr(t, "collapse all", view.Disclose(t.Context(), nil, IntentEntry{Address: address, Disclosure: Disclosure{Recursive: true}}))
	collapsed, err := frameForTest(t, view, t.Context(), FrameRequest{Limit: 1})
	testutil.FailErr(t, "read collapsed tree", err)
	if collapsed.Extent.Rows != 1 {
		t.Fatalf("collapsed extent: %+v", collapsed.Extent)
	}
}
