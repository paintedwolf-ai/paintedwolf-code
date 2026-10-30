package sourcecatalog

import (
	"fmt"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStructuralAncestorRepairSkipsUnrelatedParentRangePages(t *testing.T) {
	for _, spill := range []bool{false, true} {
		t.Run(fmt.Sprint(spill), func(t *testing.T) {
			store := checkpointTestStore(t)
			b, err := newStructuralBuilder(store, nil)
			testutil.FailErr(t, "create range repair builder", err)
			defer b.close()
			if spill {
				b.directories.memoryLimit = 0
			}
			names := []string{"achanged"}
			for sibling := range 2 * pagedview.PageFanout {
				names = append(names, fmt.Sprintf("side-%03d", sibling))
			}
			observePublicationFixture(t, b, store, ".", names, true)
			for _, name := range names {
				observePublicationFixture(t, b, store, name, []string{"old"}, false)
			}
			testutil.FailErr(t, "finalize wide parent", b.finalize(t.Context()))
			index, observation, err := b.children(t.Context(), ".")
			testutil.FailErr(t, "read wide parent", err)
			page, err := b.Read(t.Context(), index.Root)
			testutil.FailErr(t, "read parent range branches", err)
			if len(page.Children) < 2 {
				t.Fatal("fixture does not span multiple parent range pages")
			}
			page.Children = slices.Clone(page.Children)
			last := len(page.Children) - 1
			if page.Children[last].Key <= DirectoryOrder("achanged", true) {
				t.Fatal("opaque branch contains changed key")
			}
			// Keep the unrelated branch's aggregate while making its payload unreadable.
			page.Children[last].Page = structuralOffsetMask
			opaque := page.Children[last]
			index.Root, err = b.Write(t.Context(), 0, page)
			testutil.FailErr(t, "store opaque parent range branch", err)
			testutil.FailErr(t, "retain parent aggregate", b.save(t.Context(), index, observation))
			if _, err := b.Read(t.Context(), opaque.Page); err == nil {
				t.Fatalf("opaque payload unexpectedly readable: %v", err)
			}
			if err := visitStructuralDirectoryEdges(t.Context(), b, index.Root, func(pagedview.RangeItem[TreeItem]) error { return nil }); err == nil {
				t.Fatalf("full sibling enumeration did not reach opaque range page: %v", err)
			}

			for attempt := 1; attempt <= 4; attempt++ {
				observePublicationFixture(t, b, store, "achanged", []string{fmt.Sprintf("new-%d", attempt)}, false)
				testutil.FailErr(t, "repair only the changed parent range path", b.finalize(t.Context()))
				index, _, err = b.children(t.Context(), ".")
				testutil.FailErr(t, "read updated parent range", err)
				extent, err := index.Extent(t.Context())
				testutil.FailErr(t, "measure retained range aggregates", err)
				if extent != int64(2*len(names)+attempt) {
					t.Fatalf("attempt %d extent=%d", attempt, extent)
				}
				current, err := b.Read(t.Context(), index.Root)
				testutil.FailErr(t, "read updated parent branches", err)
				if len(current.Children) != len(page.Children) || current.Children[last] != opaque {
					t.Fatal("repair changed unrelated parent range branch")
				}
				item, _, err := index.LocateItem(t.Context(), DirectoryOrder("achanged", true))
				testutil.FailErr(t, "locate changed subtree summary", err)
				if item.Weight != int64(2+attempt) || item.Unresolved != 0 {
					t.Fatalf("attempt %d changed summary=%+v", attempt, item)
				}
			}
		})
	}
}
