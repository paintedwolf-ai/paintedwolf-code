package sourcecatalog

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDirectoryNamespaceReusesMembershipAndRetainsByteOrder(t *testing.T) {
	catalog, root := indexFixture(t)
	for _, rel := range []string{"a.txt", "Z/child", ".git/config", ".hidden/child", "node_modules/package/index.js"} {
		writeIndexFile(t, root.Path, rel, "source")
	}
	first, err := catalog.Directories.ReadDirectory(t.Context(), "p", root, ".", 0)
	testutil.FailErr(t, "read cold namespace", err)
	second, err := catalog.Directories.ReadDirectory(t.Context(), "p", root, ".", 0)
	testutil.FailErr(t, "reuse namespace", err)
	names := make([]string, 0, len(second.Entries))
	for _, entry := range second.Entries {
		names = append(names, entry.Name)
	}
	if first.Reused || !second.Reused || !slices.Equal(names, []string{".git", ".hidden", "Z", "a.txt", "node_modules"}) {
		t.Fatalf("first reused=%t second=%+v names=%v", first.Reused, second, names)
	}
	writeIndexFile(t, root.Path, "added", "source")
	third, err := catalog.Directories.ReadDirectory(t.Context(), "p", root, ".", 0)
	testutil.FailErr(t, "revalidate changed namespace", err)
	if third.Reused || len(third.Entries) != len(second.Entries)+1 {
		t.Fatalf("changed membership=%+v", third)
	}
}

func TestDirectoryNamespaceCannotClaimAnOmittedLink(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "kept", "source")
	testutil.FailErr(t, "create dangling link", os.Symlink("missing", filepath.Join(root.Path, "dangling")))
	_, err := catalog.Directories.ReadDirectory(t.Context(), "p", root, ".", 0)
	if !errors.Is(err, ErrDirectoryNamespace) {
		t.Fatalf("omitted link completeness=%v", err)
	}
}

func TestDirectoryNamespacePreparationRespectsConsumerBounds(t *testing.T) {
	catalog, root := indexFixture(t)
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		writeIndexFile(t, root.Path, name, "source")
	}
	listing, err := catalog.Directories.ReadDirectory(t.Context(), "p", root, ".", 2)
	var exceeded *DirectoryLimitError
	if !errors.As(err, &exceeded) || exceeded.Entries != 3 || !listing.Prepared {
		t.Fatalf("bounded preparation=%+v error=%v", listing, err)
	}
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "open bounded structure", err)
	observation, err := store.readObservation(t.Context(), ".")
	testutil.FailErr(t, "read bounded observation", err)
	if observation.Complete || observation.Entries != 3 {
		t.Fatalf("bounded observation=%+v", observation)
	}
	complete, err := catalog.Directories.ReadDirectory(t.Context(), "p", root, ".", 5)
	testutil.FailErr(t, "extend bounded observation", err)
	if len(complete.Entries) != 5 {
		t.Fatalf("complete entries=%+v", complete)
	}
}
