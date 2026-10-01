package sourcetree

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFramesKeepCompleteDeepAncestorChain(t *testing.T) {
	view, root := viewFixture(t)
	const depth = 72
	paths := []string{"."}
	dir := root.Path
	for level := 1; level <= depth; level++ {
		dir = filepath.Join(dir, fmt.Sprintf("d%d", level))
		testutil.FailErr(t, "create nested directory", os.Mkdir(dir, 0700))
		testutil.FailErr(t, "create sibling file", os.WriteFile(filepath.Join(dir, "sibling.txt"), []byte("source"), 0600))
		rel, err := filepath.Rel(root.Path, dir)
		testutil.FailErr(t, "resolve nested path", err)
		paths = append(paths, filepath.ToSlash(rel))
	}
	testutil.FailErr(t, "create deepest file", os.WriteFile(filepath.Join(dir, "target.txt"), []byte("source"), 0600))
	observeFixture(t, view, root)
	all, err := frameForTest(t, view, t.Context(), FrameRequest{Limit: 200})
	testutil.FailErr(t, "read complete fixture", err)
	anchor := Address{Root: root.ID, Path: paths[depth] + "/target.txt"}
	frame, err := frameForTest(t, view, t.Context(), FrameRequest{Anchor: &anchor, Limit: 1})
	testutil.FailErr(t, "read deep frame", err)
	assertDeepAncestors(t, frame, all.Rows, paths)

	filtered, err := view.Filter(t.Context(), "target.txt")
	testutil.FailErr(t, "filter deep tree", err)
	defer filtered.Close()
	all, err = filtered.Frame(t.Context(), FrameRequest{Limit: 200})
	testutil.FailErr(t, "read filtered fixture", err)
	frame, err = filtered.Frame(t.Context(), FrameRequest{Anchor: &anchor, Limit: 1})
	testutil.FailErr(t, "read deep filtered frame", err)
	assertDeepAncestors(t, frame, all.Rows, paths)
}

func assertDeepAncestors(t *testing.T, frame Frame, rows []Row, paths []string) {
	t.Helper()
	if len(frame.Rows) != 1 || frame.Rows[0].Name != "target.txt" || len(frame.Ancestors) != len(paths) {
		t.Fatalf("deep frame has %d rows and %d ancestors, want one row and %d ancestors", len(frame.Rows), len(frame.Ancestors), len(paths))
	}
	for depth, ancestor := range frame.Ancestors {
		if ancestor.Address.Path != paths[depth] || ancestor.Depth != depth {
			t.Fatalf("ancestor %d=%+v, want %s", depth, ancestor, paths[depth])
		}
		if ancestor.Index < 0 || ancestor.Index >= int64(len(rows)) || rows[ancestor.Index].Address != ancestor.Address {
			t.Fatalf("ancestor %s has invalid rank %d", ancestor.Address.Path, ancestor.Index)
		}
		end := ancestor.Index + 1
		for end < int64(len(rows)) && rows[end].Depth > depth {
			end++
		}
		if ancestor.End != end {
			t.Fatalf("ancestor %s ends at %d, want %d", ancestor.Address.Path, ancestor.End, end)
		}
	}
}
