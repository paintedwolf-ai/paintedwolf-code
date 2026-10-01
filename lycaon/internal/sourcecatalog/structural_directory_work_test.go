package sourcecatalog

import (
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDirectoryScratchRecordRoundTrip(t *testing.T) {
	for _, observed := range []time.Time{{}, time.Unix(1726543210, 123456789).UTC()} {
		original := structuralDirectory{page: 123456, observation: DirectoryObservation{Path: "a/\xff", Sequence: 91, FirstListed: 42, Entries: 9000, Invalidation: 77, Complete: true, Observed: observed, Failure: "read \xff failed", Epoch: repochange.Epoch{BootID: "boot", Value: 99}, Stamp: DirectoryStamp{Modified: -5, Changed: 1726543210123456789}}}
		encoded, err := encodeDirectoryWorkRecord(original)
		testutil.FailErr(t, "encode scratch directory", err)
		decoded, err := decodeDirectoryWorkRecord(original.observation.Path, encoded)
		testutil.FailErr(t, "decode scratch directory", err)
		if decoded != original {
			t.Fatalf("scratch round trip=%+v, want %+v", decoded, original)
		}
	}
	for _, body := range [][]byte{nil, make([]byte, directoryWorkScalars*8+1), append(make([]byte, directoryWorkScalars*8+1), 255)} {
		if _, err := decodeDirectoryWorkRecord("a", body); err == nil {
			t.Fatal("malformed scratch record accepted")
		}
	}
}

func TestDirectorySpillCacheTracksFlagsReplacementAndDeletion(t *testing.T) {
	workspace, err := newStructuralDirectoryWorkspace(t.TempDir(), structuralDirectoryIndex{})
	testutil.FailErr(t, "create cached workspace", err)
	defer func() { testutil.FailErr(t, "close cached workspace", workspace.Close()) }()
	workspace.memoryLimit = 0
	key := "a"
	original := structuralDirectory{observation: DirectoryObservation{Path: key, Sequence: 1}}
	testutil.FailErr(t, "write first cached value", workspace.Set(t.Context(), key, original))
	testutil.FailErr(t, "cache prior listing", workspace.SetPrevious(t.Context(), key, original))
	testutil.FailErr(t, "mark cached value dirty", workspace.UpdateFlags(t.Context(), key, directoryDirty, 0))
	testutil.FailErr(t, "consume repair flags", workspace.ClearFlags(t.Context(), directoryRepair))
	flags, err := workspace.Flags(t.Context(), key)
	testutil.FailErr(t, "reload cleared flags", err)
	if flags != directoryDirty {
		t.Fatalf("cleared flags=%d", flags)
	}
	replacement := original
	replacement.observation.Sequence = 2
	testutil.FailErr(t, "replace cached listing", workspace.Set(t.Context(), key, replacement))
	testutil.FailErr(t, "mark ancestor without changing data", workspace.UpdateFlags(t.Context(), key, directoryAncestor, 0))
	workspace.hot = nil
	got, found, err := workspace.Get(t.Context(), key)
	testutil.FailErr(t, "reload replacement after flags", err)
	if !found || got != replacement {
		t.Fatalf("flag update changed replacement: %+v", got)
	}
	previous, found, err := workspace.Previous(t.Context(), key)
	testutil.FailErr(t, "reload prior listing", err)
	if !found || previous != original {
		t.Fatalf("flag update changed prior listing: %+v", previous)
	}
	testutil.FailErr(t, "delete cached listing", workspace.DeleteSubtree(t.Context(), key))
	_, found, err = workspace.Get(t.Context(), key)
	testutil.FailErr(t, "read cached tombstone", err)
	if found {
		t.Fatal("cached listing survived deletion")
	}
	for i := 0; i < 10000; i++ {
		_, _, err = workspace.Get(t.Context(), fmt.Sprintf("missing-%05d", i))
		testutil.FailErr(t, "cache bounded negative lookup", err)
	}
	if workspace.hot.Len() > 8192 || workspace.hot.Bytes() > 8<<20 {
		t.Fatalf("hot cache exceeded its bounds: %d/%d", workspace.hot.Len(), workspace.hot.Bytes())
	}
	if len(workspace.statements) > 16 {
		t.Fatalf("unbounded prepared statement collection: %d", len(workspace.statements))
	}
}

func TestFinalizationConsumesRepairWorkBeforeLaterCoverage(t *testing.T) {
	for _, spill := range []bool{false, true} {
		t.Run(fmt.Sprint(spill), func(t *testing.T) {
			store := checkpointTestStore(t)
			builder, err := newStructuralBuilder(store, nil)
			testutil.FailErr(t, "create repair fixture", err)
			defer builder.close()
			if spill {
				builder.directories.memoryLimit = 0
			}
			observePublicationFixture(t, builder, store, ".", []string{"a", "z"}, true)
			observePublicationFixture(t, builder, store, "a", []string{"original.txt"}, false)
			observePublicationFixture(t, builder, store, "z", nil, false)
			testutil.FailErr(t, "finalize original work", builder.finalize(t.Context()))
			pending := 0
			testutil.FailErr(t, "inspect consumed work", builder.directories.VisitFlags(t.Context(), directoryRepair|directoryAncestor, false, func(string, uint64) error { pending++; return nil }))
			if pending != 0 {
				t.Fatalf("finalization left %d repair records", pending)
			}
			// A later partial-listing chunk must repair only its own ancestors.
			observePublicationFixture(t, builder, store, "z", []string{"new.txt"}, false)
			pending = 0
			testutil.FailErr(t, "inspect new coverage work", builder.directories.VisitFlags(t.Context(), directoryRepair, false, func(dir string, _ uint64) error {
				pending++
				if dir != "z" {
					return fmt.Errorf("untouched directory scheduled: %s", dir)
				}
				return nil
			}))
			if pending != 1 {
				t.Fatalf("new coverage repair records=%d", pending)
			}
			testutil.FailErr(t, "finalize new coverage", builder.finalize(t.Context()))
			children, observation, err := builder.children(t.Context(), ".")
			testutil.FailErr(t, "read updated root", err)
			extent, err := children.Extent(t.Context())
			testutil.FailErr(t, "measure updated root", err)
			unresolved, err := directoryUnresolved(t.Context(), children, stateOf(observation))
			testutil.FailErr(t, "read updated coverage", err)
			if extent != 4 || unresolved != 0 {
				t.Fatalf("updated extent/coverage=%d/%d", extent, unresolved)
			}
			frozen, err := builder.directories.freeze(t.Context(), &directoryTestPages{})
			testutil.FailErr(t, "freeze consumed flags", err)
			if frozen.Len() != 3 {
				t.Fatalf("repair consumption lost metadata: %d", frozen.Len())
			}
		})
	}
}
