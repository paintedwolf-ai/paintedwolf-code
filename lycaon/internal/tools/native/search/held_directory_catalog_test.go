//go:build darwin || linux

package search

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCatalogDiscoveryUsesTheHeldDirectoryAfterPathReplacement(t *testing.T) {
	h := newHarness(t, map[string]string{"dir/original.txt": "original"})
	h.owners.Catalog = discoveryCatalog(t)
	prepared, err := h.owners.Prepare(mustPaths(t, h), h.dispatch, requestFor(h, "find", "dir"))
	testutil.FailErr(t, "prepare find", err)
	inv := newInvocation(t.Context(), prepared, h.tc, streamsFor())
	w := newWalker(inv)
	defer w.close()
	dir, err := w.operand("dir", false)
	testutil.FailErr(t, "bind original directory", err)
	opened, err := w.openDirectory(dir)
	testutil.FailErr(t, "hold original directory", err)
	defer opened.Close()
	testutil.FailErr(t, "move original directory", os.Rename(filepath.Join(h.root, "dir"), filepath.Join(h.root, "moved")))
	testutil.FailErr(t, "create replacement directory", os.Mkdir(filepath.Join(h.root, "dir"), 0o755))
	testutil.FailErr(t, "populate replacement", os.WriteFile(filepath.Join(h.root, "dir", "replacement.txt"), []byte("replacement"), 0o644))
	entries, err := w.readChildren(dir, false, opened)
	testutil.FailErr(t, "read held membership", err)
	if len(entries) != 1 || entries[0].name != "original.txt" {
		t.Fatalf("held entries=%+v", entries)
	}
}
