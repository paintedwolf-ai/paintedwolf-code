package sourcetree

import (
	"os"
	"path/filepath"
	"slices"
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
	testutil.FailErr(t, "await shared inventory", view.catalog.Directories.AwaitNavigation(t.Context(), view.scope.Project, root))
	// Restating the disclosure derives the boundaries inline: the inventory
	// has settled, so the derivation is ready and installs synchronously.
	// Boundaries are not intent, so the restated disclosure stands. A nested
	// checkout collapses at its repository root, not at its .git directory.
	testutil.FailErr(t, "restate disclosure to derive boundaries", view.Disclose(t.Context(), nil, IntentEntry{Address: address, Disclosure: Disclosure{Open: true, Recursive: true}}))
	derived := func() []string {
		view.mu.Lock()
		defer view.mu.Unlock()
		out := []string{}
		for _, address := range view.rules.Derived() {
			out = append(out, address.Path)
		}
		slices.Sort(out)
		return out
	}
	if got, want := derived(), []string{".git", ".task", "ignored", "nested"}; !slices.Equal(got, want) {
		t.Fatalf("derived boundaries = %v, want %v", got, want)
	}
	<-view.Prepare()
	presentation := captureForTest(t, view)
	for _, rel := range paths {
		location, _, err := presentation.Locate(t.Context(), Address{Root: root.ID, Path: rel})
		testutil.FailErr(t, "locate closed boundary descendant", err)
		if location.Visible {
			t.Fatalf("root disclosure entered closed subtree %q", rel)
		}
	}
	for _, dir := range []string{".git", ".task", "ignored", "nested", "nested/.git"} {
		testutil.FailErr(t, "explicitly disclose closed subtree", view.Disclose(t.Context(), nil,
			IntentEntry{Address: Address{Root: root.ID, Path: dir}, Disclosure: Disclosure{Open: true, Recursive: true}}))
	}
	presentation = captureForTest(t, view)
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
