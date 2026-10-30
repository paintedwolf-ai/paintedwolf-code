package sourcecatalog

import (
	"fmt"
	"path"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStructuralAncestorRepairDoesNotReadSiblingSubtrees(t *testing.T) {
	for _, shape := range []struct{ width, depth int }{{2, 1}, {pagedview.PageFanout + 1, 1}, {2, 8}, {pagedview.PageFanout + 1, 8}} {
		for _, spill := range []bool{false, true} {
			t.Run(fmt.Sprintf("width=%d/depth=%d/spill=%v", shape.width, shape.depth, spill), func(t *testing.T) {
				store := checkpointTestStore(t)
				b, err := newStructuralBuilder(store, nil)
				testutil.FailErr(t, "create repair builder", err)
				defer b.close()
				if spill {
					b.directories.memoryLimit = 0
				}
				ancestors := make([]string, shape.depth)
				siblings := map[string][]string{}
				leaf := "."
				for level := range shape.depth {
					ancestors[level] = leaf
					names := []string{"changed"}
					for sibling := range shape.width {
						names = append(names, fmt.Sprintf("side-%03d", sibling))
					}
					observePublicationFixture(t, b, store, leaf, names, true)
					for _, name := range names[1:] {
						child := path.Join(leaf, name)
						siblings[leaf] = append(siblings[leaf], child)
						observePublicationFixture(t, b, store, child, []string{"retained"}, false)
					}
					leaf = path.Join(leaf, "changed")
				}
				observePublicationFixture(t, b, store, leaf, []string{"old"}, false)
				testutil.FailErr(t, "finalize initial tree", b.finalize(t.Context()))
				expected := map[string][]pagedview.RangeItem[TreeItem]{}
				for _, parent := range ancestors {
					index, _, err := b.children(t.Context(), parent)
					testutil.FailErr(t, "read initial ancestor", err)
					for _, sibling := range siblings[parent] {
						item, _, err := index.LocateItem(t.Context(), DirectoryOrder(path.Base(sibling), true))
						testutil.FailErr(t, "capture sibling summary", err)
						expected[parent] = append(expected[parent], item)
						record, found, err := b.directories.Get(t.Context(), sibling)
						testutil.FailErr(t, "read sibling metadata", err)
						if !found {
							t.Fatal("missing sibling fixture")
						}
						// Every unrelated subtree is opaque: any later page read must fail.
						record.page = structuralOffsetMask
						testutil.FailErr(t, "make sibling subtree opaque", b.directories.Set(t.Context(), sibling, record))
					}
				}
				for attempt := 1; attempt <= 4; attempt++ {
					observePublicationFixture(t, b, store, leaf, []string{fmt.Sprintf("new-%d", attempt)}, false)
					testutil.FailErr(t, "repair changed ancestor path", b.finalize(t.Context()))
					for _, parent := range ancestors {
						index, _, err := b.children(t.Context(), parent)
						testutil.FailErr(t, "read repaired ancestor", err)
						for _, before := range expected[parent] {
							after, _, err := index.LocateItem(t.Context(), before.Key)
							testutil.FailErr(t, "read retained sibling", err)
							if after != before {
								t.Fatal("unrelated sibling summary changed")
							}
						}
					}
					root, observation, err := b.children(t.Context(), ".")
					testutil.FailErr(t, "read repaired root", err)
					extent, err := root.Extent(t.Context())
					testutil.FailErr(t, "measure repaired root", err)
					want := int64(shape.depth + 2*shape.depth*shape.width + 1 + attempt)
					pending, err := directoryUnresolved(t.Context(), root, stateOf(observation))
					testutil.FailErr(t, "measure repaired coverage", err)
					if extent != want || pending != 0 {
						t.Fatalf("attempt %d root extent/coverage=%d/%d, want %d/0", attempt, extent, pending, want)
					}
					repairs := 0
					testutil.FailErr(t, "inspect consumed repair work", b.directories.VisitFlags(t.Context(), directoryRepair|directoryAncestor, false, func(string, uint64) error { repairs++; return nil }))
					if repairs != 0 {
						t.Fatalf("attempt %d retained %d pending repairs", attempt, repairs)
					}
					bytes := b.writer.TotalBytes()
					testutil.FailErr(t, "reuse completed repair", b.finalize(t.Context()))
					if b.writer.TotalBytes() != bytes {
						t.Fatal("completed repair wrote more pages")
					}
				}
			})
		}
	}
}

// A full bottom-up refresh is the reference for the incremental aggregate.
func assertStructuralFullRepairEquivalent(t *testing.T, b *structuralBuilder) {
	t.Helper()
	root, observation, err := b.children(t.Context(), ".")
	testutil.FailErr(t, "read incremental root", err)
	before, err := root.Extent(t.Context())
	testutil.FailErr(t, "measure incremental root", err)
	pending, err := directoryUnresolved(t.Context(), root, stateOf(observation))
	testutil.FailErr(t, "measure incremental coverage", err)
	fingerprint, err := DirectoryBodyFingerprint(t.Context(), root, ".", stateOf(observation), true)
	testutil.FailErr(t, "fingerprint incremental root", err)
	var paths []string
	testutil.FailErr(t, "enumerate reference directories", b.directories.Visit(t.Context(), func(d structuralDirectory) error { paths = append(paths, d.observation.Path); return nil }))
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	for _, dir := range paths {
		testutil.FailErr(t, "fully refresh "+dir, b.weighFlags(t.Context(), dir, 0))
	}
	root, observation, err = b.children(t.Context(), ".")
	testutil.FailErr(t, "read reference root", err)
	after, err := root.Extent(t.Context())
	testutil.FailErr(t, "measure reference root", err)
	afterPending, err := directoryUnresolved(t.Context(), root, stateOf(observation))
	testutil.FailErr(t, "measure reference coverage", err)
	afterFingerprint, err := DirectoryBodyFingerprint(t.Context(), root, ".", stateOf(observation), true)
	testutil.FailErr(t, "fingerprint reference root", err)
	if before != after || pending != afterPending || fingerprint != afterFingerprint {
		t.Fatalf("incremental/reference aggregate mismatch: extent %d/%d pending %d/%d fingerprint %v/%v", before, after, pending, afterPending, fingerprint, afterFingerprint)
	}
}

func TestStructuralIncrementalRepairMatchesFullRefresh(t *testing.T) {
	for _, spill := range []bool{false, true} {
		t.Run(fmt.Sprint(spill), func(t *testing.T) {
			store := checkpointTestStore(t)
			b, err := newStructuralBuilder(store, nil)
			testutil.FailErr(t, "create aggregate builder", err)
			defer b.close()
			if spill {
				b.directories.memoryLimit = 0
			}
			observePublicationFixture(t, b, store, ".", []string{"a", "z"}, true)
			observePublicationFixture(t, b, store, "a", []string{"nested"}, true)
			observePublicationFixture(t, b, store, "a/nested", []string{"one"}, false)
			observePublicationFixture(t, b, store, "z", nil, false)
			for step := 0; step < 5; step++ {
				switch step {
				case 1:
					observePublicationFixture(t, b, store, "a/nested", []string{"one", "two"}, false)
				case 2:
					testutil.FailErr(t, "start partial directory", b.directories.UpdateFlags(t.Context(), "a/nested", 0, directoryStarted))
					testutil.FailErr(t, "observe partial directory", b.observe(t.Context(), directoryDiscovery{observation: DirectoryObservation{Path: "a/nested", Entries: 1}, nodes: []indexNode{{path: "a/nested/partial", name: "partial", regular: true}}}))
				case 3:
					testutil.FailErr(t, "retain failed listing", b.retainFailedDirectory(t.Context(), DirectoryObservation{Path: "a/nested", Failure: "unavailable"}))
				case 4:
					previous, found, err := b.directories.Get(t.Context(), "a")
					testutil.FailErr(t, "capture replaced parent", err)
					if !found {
						t.Fatal("missing replacement fixture")
					}
					testutil.FailErr(t, "retain replaced parent", b.directories.SetPrevious(t.Context(), "a", previous))
					testutil.FailErr(t, "start parent replacement", b.directories.UpdateFlags(t.Context(), "a", 0, directoryStarted))
					observePublicationFixture(t, b, store, "a", nil, false)
				}
				testutil.FailErr(t, "finalize aggregate step", b.finalize(t.Context()))
				assertStructuralFullRepairEquivalent(t, b)
			}
			_, found, err := b.directories.Get(t.Context(), "a/nested")
			testutil.FailErr(t, "check removed nested metadata", err)
			if found {
				t.Fatal("removed descendant survived complete parent replacement")
			}
		})
	}
}
