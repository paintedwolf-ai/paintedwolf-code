package sourcecatalog

import (
	"fmt"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStructuralDirectorySnapshotsSupportConcurrentReadersAndCopyOnWrite(t *testing.T) {
	store := checkpointTestStore(t)
	builder, err := newStructuralBuilder(store, nil)
	testutil.FailErr(t, "create concurrent directory fixture", err)
	defer builder.close()
	const count = 1024
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("dir-%04d", i)
		testutil.FailErr(t, "stage immutable directory", builder.directories.Set(t.Context(), key, structuralDirectory{observation: DirectoryObservation{Path: key, Sequence: int64(i + 1)}}))
	}
	generation, err := builder.seal(t.Context(), 1)
	testutil.FailErr(t, "seal concurrent directory fixture", err)
	defer generation.close()
	pages := generation.directories.store.(*structuralDirectoryPages)
	pages.cache = pagedview.NewCache[uint64, pagedview.RangePage[structuralDirectory]](4, 1<<20)
	start := make(chan struct{})
	failures := make(chan error, 9)
	var workers sync.WaitGroup
	for reader := 0; reader < 8; reader++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for iteration := 0; iteration < 128; iteration++ {
				at := (reader*137 + iteration*17) % count
				key := fmt.Sprintf("dir-%04d", at)
				record, found, err := generation.directories.Get(t.Context(), key)
				if err != nil {
					failures <- err
					return
				}
				if !found || record.observation.Sequence != int64(at+1) {
					failures <- fmt.Errorf("immutable directory %s changed: %+v", key, record)
					return
				}
				if iteration%32 == 0 {
					visited := 0
					err = generation.directories.Visit(t.Context(), func(record structuralDirectory) error {
						expected := fmt.Sprintf("dir-%04d", visited)
						if record.observation.Path != expected || record.observation.Sequence != int64(visited+1) {
							return fmt.Errorf("immutable traversal changed at %s", expected)
						}
						visited++
						return nil
					})
					if err != nil {
						failures <- err
						return
					}
					if visited != count {
						failures <- fmt.Errorf("immutable traversal count=%d", visited)
						return
					}
				}
			}
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		<-start
		next, err := newStructuralBuilder(store, generation)
		if err != nil {
			failures <- err
			return
		}
		defer next.close()
		for i := 0; i < 128; i++ {
			key := fmt.Sprintf("dir-%04d", i)
			if err := next.directories.Set(t.Context(), key, structuralDirectory{observation: DirectoryObservation{Path: key, Sequence: 9999}}); err != nil {
				failures <- err
				return
			}
		}
		changed, err := next.seal(t.Context(), 2)
		if err != nil {
			failures <- err
			return
		}
		defer changed.close()
		record, found, err := changed.directories.Get(t.Context(), "dir-0000")
		if err != nil {
			failures <- err
			return
		}
		if !found || record.observation.Sequence != 9999 {
			failures <- fmt.Errorf("copy-on-write update missing: %+v", record)
		}
	}()
	close(start)
	workers.Wait()
	close(failures)
	for err := range failures {
		testutil.FailErr(t, "concurrent immutable directory access", err)
	}
}

func TestStructuralDirectoryPageCacheOwnsWriteBuffers(t *testing.T) {
	store := checkpointTestStore(t)
	builder, err := newStructuralBuilder(store, nil)
	testutil.FailErr(t, "create directory page owner", err)
	defer builder.close()
	pages := builder.directoryPages
	leaf := pagedview.RangePage[structuralDirectory]{Items: []pagedview.RangeItem[structuralDirectory]{{Key: "before", Value: structuralDirectory{observation: DirectoryObservation{Path: "before"}}, Weight: 1}}}
	leafID, err := pages.Write(t.Context(), 0, leaf)
	testutil.FailErr(t, "cache directory leaf", err)
	leaf.Items[0].Key = "after"
	leaf.Items[0].Value.observation.Path = "after"
	saved, err := pages.Read(t.Context(), leafID)
	testutil.FailErr(t, "read independently owned leaf", err)
	if saved.Items[0].Key != "before" || saved.Items[0].Value.observation.Path != "before" {
		t.Fatal("cached leaf aliases caller write buffer")
	}
	branch := pagedview.RangePage[structuralDirectory]{Children: []pagedview.Branch{{Key: "before", Page: leafID, Count: 1, Weight: 1}}}
	branchID, err := pages.Write(t.Context(), 0, branch)
	testutil.FailErr(t, "cache directory branch", err)
	branch.Children[0].Key = "after"
	saved, err = pages.Read(t.Context(), branchID)
	testutil.FailErr(t, "read independently owned branch", err)
	if saved.Children[0].Key != "before" {
		t.Fatal("cached branch aliases caller write buffer")
	}
}
