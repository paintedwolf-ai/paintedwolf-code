package sourcecatalog

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDirectoryBoundaryProjectionUsesObservedKind(t *testing.T) {
	catalog, root := indexFixture(t)
	catalog.SetScopes(testScopes{plane: sourcescope.Plane{BoundaryDirectories: []string{"curated-file", "curated-dir"}, NestedCheckouts: true}})
	for _, rel := range []string{"curated-file", "plain-file", "curated-dir/leaf", "checkout/.git"} {
		writeIndexFile(t, root.Path, rel, "source")
	}
	want := map[string]struct{ directory, boundary bool }{
		"curated-file": {false, false}, "plain-file": {false, false},
		"curated-dir": {true, true}, "checkout": {true, true},
	}
	reader := catalog.Directories.NewDirectoryReader("p", root)
	defer reader.Close(t.Context())
	for attempt := range 2 {
		listing, err := reader.ReadDirectory(t.Context(), ".", 0, nil)
		testutil.FailErr(t, "project directory membership", err)
		if listing.Reused != (attempt == 1) || len(listing.Entries) != len(want) {
			t.Fatalf("attempt %d reused=%t entries=%d", attempt, listing.Reused, len(listing.Entries))
		}
		for _, entry := range listing.Entries {
			expected, ok := want[entry.Name]
			if !ok || entry.IsDir != expected.directory || entry.Boundary != expected.boundary {
				t.Fatalf("attempt %d projected entry=%+v want=%+v", attempt, entry, expected)
			}
		}
	}
	testutil.FailErr(t, "publish projected membership", reader.Close(t.Context()))
	navigation, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "pin observed namespace", err)
	defer navigation.Close()
	testutil.FailErr(t, "replace observed file", os.Remove(filepath.Join(root.Path, "plain-file")))
	writeIndexFile(t, root.Path, "plain-file/.git", "gitdir: ../repository")
	if reason := navigation.pin.store.policy.boundaryDir("plain-file"); reason != sourcescope.BoundaryNestedCheckout {
		t.Fatalf("fixture live directory boundary=%q", reason)
	}
	for name, expected := range want {
		entry, err := navigation.Entry(t.Context(), name)
		testutil.FailErr(t, "project pinned entry kind", err)
		if entry.IsDir != expected.directory || entry.Boundary != expected.boundary {
			t.Fatalf("pinned entry=%+v want=%+v", entry, expected)
		}
	}
}
