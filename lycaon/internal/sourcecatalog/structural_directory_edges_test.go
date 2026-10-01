package sourcecatalog

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

type structuralEdgePages struct {
	pages map[uint64]pagedview.RangePage[TreeItem]
	reads []uint64
}

func (s *structuralEdgePages) Read(_ context.Context, id uint64) (pagedview.RangePage[TreeItem], error) {
	s.reads = append(s.reads, id)
	page, found := s.pages[id]
	if !found {
		return page, errors.New("unexpected page read")
	}
	return page, nil
}
func (*structuralEdgePages) Write(context.Context, uint64, pagedview.RangePage[TreeItem]) (uint64, error) {
	return 0, errors.New("unexpected page write")
}
func (*structuralEdgePages) Delete(context.Context, uint64) error {
	return errors.New("unexpected page delete")
}

func TestStructuralDirectoryEdgesSkipFileOnlyBranches(t *testing.T) {
	dirKey, fileKey := DirectoryOrder("child", true), DirectoryOrder("file", false)
	pages := &structuralEdgePages{pages: map[uint64]pagedview.RangePage[TreeItem]{
		1: {Children: []pagedview.Branch{{Key: dirKey, Page: 2}, {Key: fileKey, Page: 99}}},
		2: {Children: []pagedview.Branch{{Key: dirKey, Page: 3}, {Key: fileKey, Page: 98}}},
		3: {Items: []pagedview.RangeItem[TreeItem]{{Key: dirKey, Value: TreeItem{Path: "child"}}, {Key: fileKey}}},
	}}
	var visited []string
	err := visitStructuralDirectoryEdges(t.Context(), pages, 1, func(item pagedview.RangeItem[TreeItem]) error {
		visited = append(visited, item.Value.Path)
		return nil
	})
	testutil.FailErr(t, "visit directory prefix", err)
	if !reflect.DeepEqual(visited, []string{"child"}) || !reflect.DeepEqual(pages.reads, []uint64{1, 2, 3}) {
		t.Fatalf("visited %v using pages %v", visited, pages.reads)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = visitStructuralDirectoryEdges(ctx, pages, 1, func(pagedview.RangeItem[TreeItem]) error { return nil })
	if !errors.Is(err, context.Canceled) || len(pages.reads) != 3 {
		t.Fatalf("canceled walk read pages: %v, %v", err, pages.reads)
	}
}

func TestStructuralFileOnlyRepairNeedsNoPageRead(t *testing.T) {
	workspace, err := newStructuralDirectoryWorkspace(t.TempDir(), structuralDirectoryIndex{})
	testutil.FailErr(t, "create file-only workspace", err)
	defer func() { _ = workspace.Close() }()
	builder := &structuralBuilder{directories: workspace}
	testutil.FailErr(t, "record file-only coverage", workspace.UpdateFlags(t.Context(), "files", directoryBranchesKnown, 0))
	// An invalid page makes an unnecessary read fail.
	testutil.FailErr(t, "save file-only metadata", builder.directories.Set(t.Context(), "files", structuralDirectory{page: 7, observation: DirectoryObservation{Path: "files"}}))
	testutil.FailErr(t, "skip file-only repair", builder.weighFlags(t.Context(), "files", directoryBranchesKnown))
}

func TestStructuralFinalizeIsReusableUntilAnotherObservation(t *testing.T) {
	catalog, root := indexFixture(t)
	store, err := catalog.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get finalization store", err)
	builder, err := newStructuralBuilder(store, nil)
	testutil.FailErr(t, "create finalization builder", err)
	defer builder.close()
	observePublicationFixture(t, builder, store, ".", []string{"child"}, true)
	observePublicationFixture(t, builder, store, "child", []string{"file"}, false)
	testutil.FailErr(t, "finalize initial observations", builder.finalize(t.Context()))
	bytes := builder.writer.TotalBytes()
	testutil.FailErr(t, "reuse finalized observations", builder.finalize(t.Context()))
	if builder.writer.TotalBytes() != bytes {
		t.Fatal("repeated finalization wrote pages")
	}
	observePublicationFixture(t, builder, store, "child", []string{"second"}, false)
	if builder.finalized {
		t.Fatal("new observation reused old finalization")
	}
	testutil.FailErr(t, "repair additional child", builder.finalize(t.Context()))
	index, _, err := builder.children(t.Context(), ".")
	testutil.FailErr(t, "read repaired directory", err)
	item, _, err := index.LocateItem(t.Context(), DirectoryOrder("child", true))
	testutil.FailErr(t, "read repaired parent", err)
	if item.Weight != 3 || item.Unresolved != 0 {
		t.Fatalf("parent measure = %d, unresolved = %d", item.Weight, item.Unresolved)
	}
}
