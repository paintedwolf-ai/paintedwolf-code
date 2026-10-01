//go:build stress

package sourcecatalog

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStressStructuralInventoryRanksAndRetainedExtents(t *testing.T) {
	catalog, root := indexFixture(t)
	store, err := catalog.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "open structural catalog", err)
	const directories, files = 1000, 1000
	parents := make([]indexNode, directories)
	for i := range parents {
		name := fmt.Sprintf("dir-%04d", i)
		parents[i] = indexNode{path: name, parent: ".", name: name, isDir: true}
	}
	pin, err := store.retainGeneration(headGeneration, true)
	testutil.FailErr(t, "pin empty generation", err)
	builder, err := newStructuralBuilder(store, pin.value)
	testutil.FailErr(t, "create directory builder", err)
	testutil.FailErr(t, "build directory inventory", builder.observe(t.Context(), directoryDiscovery{
		nodes: parents, observation: DirectoryObservation{Path: ".", Entries: directories, Complete: true, Invalidation: store.observationMark(".")},
	}))
	testutil.FailErr(t, "finalize directory inventory", builder.finalize(t.Context()))
	testutil.FailErr(t, "publish directory inventory", store.publishStructure(t.Context(), builder, pin.Generation))
	builder.close()
	pin.Release()
	initial, err := catalog.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "retain initial extent", err)
	defer func() { _ = initial.Close() }()
	pin, err = store.retainGeneration(headGeneration, true)
	testutil.FailErr(t, "pin directory generation", err)
	builder, err = newStructuralBuilder(store, pin.value)
	testutil.FailErr(t, "create file builder", err)
	for start := 0; start < directories; start += 20 {
		for _, parent := range parents[start : start+20] {
			nodes := make([]indexNode, files)
			for i := range nodes {
				name := fmt.Sprintf("file-%04d.txt", i)
				nodes[i] = indexNode{path: parent.path + "/" + name, parent: parent.path, name: name, regular: true}
			}
			testutil.FailErr(t, "build file inventory", builder.observe(t.Context(), directoryDiscovery{nodes: nodes,
				observation: DirectoryObservation{Path: parent.path, Entries: files, Complete: true, Invalidation: store.observationMark(parent.path)}}))
		}
	}
	testutil.FailErr(t, "finalize file inventory", builder.finalize(t.Context()))
	testutil.FailErr(t, "publish file inventory", store.publishStructure(t.Context(), builder, pin.Generation))
	builder.close()
	pin.Release()
	complete, err := catalog.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "retain complete inventory", err)
	defer func() { _ = complete.Close() }()
	const total = directories * (files + 1)
	if first, last := navigationExtent(t, initial), navigationExtent(t, complete); first != directories || last != total {
		t.Fatalf("initial=%d complete=%d", first, last)
	}
	for _, rank := range []int64{total - 1, total / 2, 1, total * 3 / 4, files + 1} {
		entry, offset, err := complete.Child(t.Context(), ".", rank, true)
		testutil.FailErr(t, "seek expanded directory", err)
		want := fmt.Sprintf("dir-%04d", rank/(files+1))
		if entry.Path != want || offset != rank%(files+1) {
			t.Fatalf("rank %d selected %s+%d", rank, entry.Path, offset)
		}
		if offset > 0 {
			entry, _, err = complete.Child(t.Context(), entry.Path, offset-1, false)
			testutil.FailErr(t, "seek file within directory", err)
			if expected := fmt.Sprintf("%s/file-%04d.txt", want, offset-1); entry.Path != expected {
				t.Fatalf("rank %d selected %s, want %s", rank, entry.Path, expected)
			}
		}
	}
	observation, err := store.readObservation(t.Context(), parents[0].path)
	testutil.FailErr(t, "read changed directory", err)
	observation.Entries++
	added := indexNode{path: parents[0].path + "/z-last.txt", parent: parents[0].path, name: "z-last.txt", regular: true}
	updatedNodes := make([]indexNode, 0, files+1)
	for i := range files {
		name := fmt.Sprintf("file-%04d.txt", i)
		updatedNodes = append(updatedNodes, indexNode{path: parents[0].path + "/" + name, parent: parents[0].path, name: name, regular: true})
	}
	updatedNodes = append(updatedNodes, added)
	pin, err = store.retainGeneration(headGeneration, true)
	testutil.FailErr(t, "pin complete generation", err)
	builder, err = newStructuralBuilder(store, pin.value)
	testutil.FailErr(t, "create update builder", err)
	testutil.FailErr(t, "build added file", builder.observe(t.Context(), directoryDiscovery{nodes: updatedNodes, observation: observation}))
	testutil.FailErr(t, "finalize added file", builder.finalize(t.Context()))
	testutil.FailErr(t, "publish added file", store.publishStructure(t.Context(), builder, pin.Generation))
	builder.close()
	pin.Release()
	updated, err := catalog.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "read updated extent", err)
	defer func() { _ = updated.Close() }()
	if old, next := navigationExtent(t, complete), navigationExtent(t, updated); old != total || next != total+1 {
		t.Fatalf("retained=%d updated=%d", old, next)
	}
	entry, _, err := updated.Child(t.Context(), parents[0].path, files, false)
	testutil.FailErr(t, "read added file by rank", err)
	if entry.Path != added.path {
		t.Fatalf("added rank selected %s", entry.Path)
	}
}
