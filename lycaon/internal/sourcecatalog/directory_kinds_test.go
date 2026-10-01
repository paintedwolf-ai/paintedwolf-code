//go:build darwin || linux

package sourcecatalog

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestDirectoryOpenRejectsRegularFilesAndPipes(t *testing.T) {
	rootPath := t.TempDir()
	writeIndexFile(t, rootPath, "file", "source")
	testutil.FailErr(t, "create named pipe", unix.Mkfifo(filepath.Join(rootPath, "pipe"), 0o600))
	root, err := os.OpenRoot(rootPath)
	testutil.FailErr(t, "open root", err)
	defer func() { _ = root.Close() }()
	for _, name := range []string{"file", "pipe"} {
		file, err := openDirectoryFile(root, name)
		if file != nil {
			_ = file.Close()
		}
		if !errors.Is(err, os.ErrInvalid) {
			t.Fatalf("open %s as directory: %v", name, err)
		}
	}
}

func TestDirectoryKindsRetainsTheOpenedDirectory(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "directory/file.txt", "source")
	writeIndexFile(t, root.Path, "directory/nested/child.txt", "source")
	testutil.FailErr(t, "create directory link", os.Symlink("nested", filepath.Join(root.Path, "directory", "link")))
	store, err := catalog.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get catalog", err)
	physical, release, err := store.acquireNavigation(t.Context())
	testutil.FailErr(t, "open root", err)
	defer release()
	directory, err := catalog.openObservationDirectory(t.Context(), store, physical, "directory")
	testutil.FailErr(t, "open confined directory", err)
	defer directory.close()
	outside := t.TempDir()
	writeIndexFile(t, outside, "outside.txt", "outside")
	testutil.FailErr(t, "move opened directory", os.Rename(filepath.Join(root.Path, "directory"), filepath.Join(root.Path, "moved")))
	testutil.FailErr(t, "replace directory with outside link", os.Symlink(outside, filepath.Join(root.Path, "directory")))
	entries, err := directory.entries.ReadDir(-1)
	testutil.FailErr(t, "read retained descriptor", err)
	kinds := make(map[string]os.FileMode)
	for _, entry := range entries {
		kinds[entry.Name()] = entry.Type()
	}
	if len(kinds) != 3 || kinds["nested"] != os.ModeDir || kinds["link"] != os.ModeSymlink {
		t.Fatalf("unexpected directory kinds: %v", kinds)
	}
	if kind, ok := kinds["file.txt"]; !ok || !kind.IsRegular() {
		t.Fatalf("missing regular file: %v", kinds)
	}
}
