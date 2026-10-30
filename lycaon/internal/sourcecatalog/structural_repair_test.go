package sourcecatalog

import (
	"fmt"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStructuralAncestorRepairDoesNotReadSiblingSubtrees(t *testing.T) {
	for _, spill := range []bool{false, true} {
		t.Run(fmt.Sprint(spill), func(t *testing.T) {
			store := checkpointTestStore(t)
			builder, err := newStructuralBuilder(store, nil)
			testutil.FailErr(t, "create repair builder", err)
			defer builder.close()
			if spill {
				builder.directories.memoryLimit = 0
			}
			observePublicationFixture(t, builder, store, ".", []string{"changed", "unrelated"}, true)
			observePublicationFixture(t, builder, store, "changed", []string{"leaf"}, true)
			observePublicationFixture(t, builder, store, "changed/leaf", []string{"old"}, false)
			observePublicationFixture(t, builder, store, "unrelated", []string{"retained"}, false)
			testutil.FailErr(t, "finalize initial tree", builder.finalize(t.Context()))
			root, _, err := builder.children(t.Context(), ".")
			testutil.FailErr(t, "read initial root", err)
			before, _, err := root.LocateItem(t.Context(), DirectoryOrder("unrelated", true))
			testutil.FailErr(t, "capture sibling summary", err)
			record, found, err := builder.directories.Get(t.Context(), "unrelated")
			testutil.FailErr(t, "read sibling metadata", err)
			if !found {
				t.Fatal("missing sibling fixture")
			}
			record.page = structuralOffsetMask
			testutil.FailErr(t, "poison unrelated subtree page", builder.directories.Set(t.Context(), "unrelated", record))
			// Completed repairs must not reopen unrelated subtrees.
			observePublicationFixture(t, builder, store, "changed/leaf", []string{"new", "second"}, false)
			testutil.FailErr(t, "repair changed ancestor path", builder.finalize(t.Context()))
			root, _, err = builder.children(t.Context(), ".")
			testutil.FailErr(t, "read repaired root", err)
			after, _, err := root.LocateItem(t.Context(), before.Key)
			testutil.FailErr(t, "read retained sibling", err)
			if after != before {
				t.Fatal("unrelated sibling summary changed")
			}
			changed, _, err := root.LocateItem(t.Context(), DirectoryOrder("changed", true))
			testutil.FailErr(t, "read repaired child", err)
			if changed.Weight != 5 || changed.Unresolved != 0 {
				t.Fatalf("changed summary=%+v", changed)
			}
		})
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
