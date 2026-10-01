package sourcetree

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPresentationSurvivesIntentAndStructureChanges(t *testing.T) {
	view, root := viewFixture(t)
	for i := range 6 {
		directory := filepath.Join(root.Path, fmt.Sprintf("dir-%d", i))
		testutil.FailErr(t, "create directory", os.Mkdir(directory, 0700))
		testutil.FailErr(t, "create file", os.WriteFile(filepath.Join(directory, "file.txt"), []byte("source"), 0600))
	}
	address := Address{Root: root.ID, Path: "."}
	testutil.FailErr(t, "expand", view.Disclose(t.Context(), nil, IntentEntry{Address: address, Disclosure: Disclosure{Open: true, Recursive: true}}))
	expanded := captureForTest(t, view)
	_, extent := expanded.Revision()
	if extent.Rows != 13 || !extent.Complete {
		t.Fatalf("extent=%+v", extent)
	}
	testutil.FailErr(t, "collapse", view.Toggle(t.Context(), nil, address, true))
	collapsed := captureForTest(t, view)
	_, small := collapsed.Revision()
	if small.Rows != 7 {
		t.Fatalf("collapsed extent=%+v", small)
	}
	testutil.FailErr(t, "delete directory", os.RemoveAll(filepath.Join(root.Path, "dir-5")))
	view.catalog.InvalidateRoot(root.Path, "dir-5")
	_, err := view.catalog.ObserveDirectory(t.Context(), view.scope.Project, root, ".", sourcecatalog.DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "publish deletion", err)
	current := captureForTest(t, view)
	_, latest := current.Revision()
	if latest.Rows != 6 {
		t.Fatalf("latest extent=%+v", latest)
	}
	for _, offset := range []int64{11, 0, 7, 12, 3} {
		frame, err := expanded.Frame(t.Context(), FrameRequest{Offset: offset, Limit: 2})
		testutil.FailErr(t, "read retained expansion", err)
		if frame.Extent != extent {
			t.Fatalf("extent changed: %+v", frame.Extent)
		}
		if offset == 12 && frame.Rows[0].Address.Path != "dir-5/file.txt" {
			t.Fatalf("deleted row lost: %+v", frame.Rows)
		}
	}
	frame, err := collapsed.Frame(t.Context(), FrameRequest{Offset: 6, Limit: 2})
	testutil.FailErr(t, "read retained collapse", err)
	if frame.Rows[0].Address.Path != "dir-5" || frame.Extent != small {
		t.Fatalf("collapsed coordinates changed: %+v", frame)
	}
}

func TestPresentationFrameSurvivesCatalogClearAndRebuild(t *testing.T) {
	view, root := viewFixture(t)
	testutil.FailErr(t, "create retained file", os.WriteFile(filepath.Join(root.Path, "retained.txt"), []byte("old"), 0o600))
	observeFixture(t, view, root)
	presentation := captureForTest(t, view)
	before, err := presentation.Frame(t.Context(), FrameRequest{Offset: 0, Limit: 10})
	testutil.FailErr(t, "read frame before clear", err)

	testutil.FailErr(t, "clear source catalog", view.catalog.ClearTreeStores(t.Context(), func() error {
		return os.RemoveAll(enginepaths.SourceCatalogCacheRootUnder(os.Getenv("LYCAON_CONFIG_DIR")))
	}))
	testutil.FailErr(t, "remove retained file", os.Remove(filepath.Join(root.Path, "retained.txt")))
	testutil.FailErr(t, "create replacement file", os.WriteFile(filepath.Join(root.Path, "replacement.txt"), []byte("new"), 0o600))
	_, err = view.catalog.ObserveDirectory(t.Context(), view.scope.Project, root, ".", sourcecatalog.DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "rebuild catalog head", err)

	after, err := presentation.Frame(t.Context(), FrameRequest{Offset: 0, Limit: 10})
	testutil.FailErr(t, "read retained frame after clear", err)
	if len(after.Rows) != len(before.Rows) || after.Extent != before.Extent {
		t.Fatalf("retained frame extent changed after catalog rebuild: before=%+v after=%+v", before, after)
	}
	for i := range before.Rows {
		if after.Rows[i].Address != before.Rows[i].Address {
			t.Fatalf("retained row %d changed after catalog rebuild: before=%+v after=%+v", i, before.Rows[i], after.Rows[i])
		}
	}
}
