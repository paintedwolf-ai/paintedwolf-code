//go:build stress

package sourcecatalog

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Directory metadata spills independently of file rows.
func TestStressStructuralDirectoryMetadataBeyondResidentThreshold(t *testing.T) {
	const directories = (128<<20)/256 + 1
	store := checkpointTestStore(t)
	store.stores.trees = map[string]projectionStore{"large-directories": store}
	t.Cleanup(func() { testutil.FailErr(t, "drain large directory fixture", store.stores.Drain(t.Context())) })
	builder, err := newStructuralBuilder(store, nil)
	testutil.FailErr(t, "create large directory builder", err)
	defer builder.close()
	children := &pagedview.RangeIndex[TreeItem]{Store: builder}
	empty := &pagedview.RangeIndex[TreeItem]{Store: builder}
	state := DirectoryState{Listed: true, Complete: true}
	err = children.BuildSorted(t.Context(), func(yield func(pagedview.RangeItem[TreeItem]) error) error {
		for i := 0; i < directories; i++ {
			name := fmt.Sprintf("dir-%07d", i)
			observation := DirectoryObservation{Path: name, Sequence: 1, FirstListed: 1, Complete: true}
			if err := builder.save(t.Context(), empty, observation); err != nil {
				return err
			}
			body, err := DirectoryBodyFingerprint(t.Context(), empty, name, state, true)
			if err != nil {
				return err
			}
			item := pagedview.RangeItem[TreeItem]{Key: DirectoryOrder(name, true), Value: TreeItem{Path: name, Sequence: 1}, Weight: 2,
				BaselineFingerprint: TreeRowFingerprint(name, "directory", false, false, ""),
				Fingerprint:         TreeRowFingerprint(name, "directory", false, true, "").Combine(body)}
			if err := yield(item); err != nil {
				return err
			}
		}
		return nil
	})
	testutil.FailErr(t, "stream directory inventory", err)
	testutil.FailErr(t, "save large root", builder.save(t.Context(), children, DirectoryObservation{Path: ".", Sequence: 1, FirstListed: 1, Complete: true, Entries: directories}))
	if builder.directories.tx == nil || builder.directories.memory != nil {
		t.Fatal("large directory inventory did not spill")
	}
	generation, err := builder.seal(t.Context(), 1)
	testutil.FailErr(t, "publish spilled directory pages", err)
	store.mu.Lock()
	store.initializeStructureLocked()
	store.installStructureLocked(generation)
	store.mu.Unlock()
	navigation, err := openNavigation(t.Context(), store, headGeneration)
	testutil.FailErr(t, "open spilled published navigation", err)
	defer func() { testutil.FailErr(t, "close spilled navigation", navigation.Close()) }()
	if generation.directories.Len() != directories+1 {
		t.Fatalf("published directory metadata=%d", generation.directories.Len())
	}
	if extent := navigationExtent(t, navigation); extent != directories*2 {
		t.Fatalf("published extent=%d", extent)
	}
	for _, rank := range []int64{0, directories / 2 * 2, directories*2 - 1} {
		entry, offset, err := navigation.Child(t.Context(), ".", rank, true)
		testutil.FailErr(t, "seek large directory snapshot", err)
		want := fmt.Sprintf("dir-%07d", rank/2)
		if entry.Path != want || offset != rank%2 {
			t.Fatalf("rank %d = %s+%d, want %s+%d", rank, entry.Path, offset, want, rank%2)
		}
		state, err := navigation.State(t.Context(), entry.Path)
		testutil.FailErr(t, "lookup spilled directory state", err)
		if !state.Listed || !state.Complete {
			t.Fatalf("directory %s state=%+v", entry.Path, state)
		}
	}
}
