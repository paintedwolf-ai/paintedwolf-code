package sourcecatalog

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

type directoryTestPages struct {
	pagedview.MemoryPages[structuralDirectory]
}

func (pages *directoryTestPages) Write(ctx context.Context, _ uint64, page pagedview.RangePage[structuralDirectory]) (uint64, error) {
	return pages.MemoryPages.Write(ctx, 0, page)
}
func (*directoryTestPages) Delete(context.Context, uint64) error { return nil }

type countedDirectoryTestPages struct {
	directoryTestPages
	reads int
}

func (pages *countedDirectoryTestPages) Read(ctx context.Context, id uint64) (pagedview.RangePage[structuralDirectory], error) {
	pages.reads++
	return pages.directoryTestPages.Read(ctx, id)
}

func TestStructuralDirectoryChildrenSeekPastDescendants(t *testing.T) {
	workspace, err := newStructuralDirectoryWorkspace(t.TempDir(), structuralDirectoryIndex{})
	testutil.FailErr(t, "create child traversal workspace", err)
	defer func() { _ = workspace.Close() }()
	paths := []string{".", "!", "-", "a", "a!", "a-b", "a/!", "a/-", "a/deep", "a/deep-branch", "a/deep0", "a0", "ab", "z"}
	for i := range 12000 {
		paths = append(paths, fmt.Sprintf("a/deep/child-%05d", i))
	}
	for _, key := range paths {
		testutil.FailErr(t, "stage child traversal metadata", workspace.Set(t.Context(), key, structuralDirectory{observation: DirectoryObservation{Path: key}}))
	}
	pages := &countedDirectoryTestPages{}
	index, err := workspace.freeze(t.Context(), pages)
	testutil.FailErr(t, "freeze child traversal metadata", err)
	for _, fixture := range []struct {
		parent string
		want   []string
	}{
		{".", []string{"!", "-", "a", "a!", "a-b", "a0", "ab", "z"}},
		{"a", []string{"a/!", "a/-", "a/deep", "a/deep-branch", "a/deep0"}},
		{"missing", nil},
	} {
		t.Run(fixture.parent, func(t *testing.T) {
			pages.reads = 0
			var got []string
			testutil.FailErr(t, "visit direct directory children", index.VisitChildren(t.Context(), fixture.parent, func(value structuralDirectory) error {
				got = append(got, value.observation.Path)
				return nil
			}))
			if !reflect.DeepEqual(got, fixture.want) {
				t.Fatalf("direct children=%q, want %q", got, fixture.want)
			}
			if pages.reads > 40 {
				t.Fatalf("child traversal read %d pages across skipped descendants", pages.reads)
			}
		})
	}
	canceled, cancel := context.WithCancel(t.Context())
	err = index.VisitChildren(canceled, ".", func(structuralDirectory) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled child traversal=%v", err)
	}
	stop := errors.New("stop child traversal")
	if err := index.VisitChildren(t.Context(), ".", func(structuralDirectory) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("child traversal callback error=%v", err)
	}
}

func TestStructuralDirectoryWorkspaceSpillAndPackedSnapshot(t *testing.T) {
	workspace, err := newStructuralDirectoryWorkspace(t.TempDir(), structuralDirectoryIndex{})
	testutil.FailErr(t, "create directory workspace", err)
	defer func() { testutil.FailErr(t, "close directory workspace", workspace.Close()) }()
	workspace.memoryLimit = 2048
	const count = 3000
	for i := count - 1; i >= 0; i-- {
		key := fmt.Sprintf("dir-%05d", i)
		value := structuralDirectory{observation: DirectoryObservation{Path: key, Sequence: int64(i + 1), FirstListed: 1, Complete: true}}
		testutil.FailErr(t, "stage directory metadata", workspace.Set(t.Context(), key, value))
		testutil.FailErr(t, "stage directory work", workspace.UpdateFlags(t.Context(), key, directoryDirty|directoryBranchesKnown, 0))
	}
	if workspace.tx == nil || workspace.memory != nil || workspace.bytes != 0 {
		t.Fatal("directory workspace did not release its spilled map")
	}
	pages := &directoryTestPages{}
	index, err := workspace.freeze(t.Context(), pages)
	testutil.FailErr(t, "pack spilled directories", err)
	if index.Len() != count {
		t.Fatalf("directory count=%d", index.Len())
	}
	for _, i := range []int{0, 128, 2999} {
		key := fmt.Sprintf("dir-%05d", i)
		record, found, err := index.Get(t.Context(), key)
		testutil.FailErr(t, "lookup packed directory", err)
		if !found || record.observation.Sequence != int64(i+1) {
			t.Fatalf("directory %q = %+v, found %t", key, record, found)
		}
	}
	visited := 0
	testutil.FailErr(t, "visit staged work in reverse", workspace.VisitFlags(t.Context(), directoryDirty, true, func(key string, flags uint64) error {
		want := fmt.Sprintf("dir-%05d", count-1-visited)
		if key != want || flags&directoryBranchesKnown == 0 {
			t.Fatalf("work %q/%d, want %q", key, flags, want)
		}
		visited++
		return nil
	}))
	if visited != count {
		t.Fatalf("visited %d work records", visited)
	}
	again, err := workspace.freeze(t.Context(), pages)
	testutil.FailErr(t, "reuse frozen directory index", err)
	if again.root != index.root {
		t.Fatal("unchanged workspace repacked metadata")
	}
}

func TestStructuralDirectoryWorkspacePrefixDeletionPreservesSnapshot(t *testing.T) {
	for _, spilled := range []bool{false, true} {
		t.Run(fmt.Sprint(spilled), func(t *testing.T) {
			first, err := newStructuralDirectoryWorkspace(t.TempDir(), structuralDirectoryIndex{})
			testutil.FailErr(t, "create original workspace", err)
			defer func() { _ = first.Close() }()
			pages := &directoryTestPages{}
			paths := []string{".", "a", "a/child", "a/child/deep", "ab", "ab/child", string([]byte{'z', 0xff})}
			for _, key := range paths {
				testutil.FailErr(t, "add initial metadata", first.Set(t.Context(), key, structuralDirectory{observation: DirectoryObservation{Path: key, Sequence: 1}}))
			}
			original, err := first.freeze(t.Context(), pages)
			testutil.FailErr(t, "freeze original metadata", err)
			next, err := newStructuralDirectoryWorkspace(t.TempDir(), original)
			testutil.FailErr(t, "create delta workspace", err)
			defer func() { _ = next.Close() }()
			if spilled {
				next.memoryLimit = 0
			}
			testutil.FailErr(t, "remove directory subtree", next.DeleteSubtree(t.Context(), "a"))
			testutil.FailErr(t, "replace surviving metadata", next.Set(t.Context(), "ab", structuralDirectory{observation: DirectoryObservation{Path: "ab", Sequence: 2}}))
			testutil.FailErr(t, "add new descendant", next.Set(t.Context(), "ab/new", structuralDirectory{observation: DirectoryObservation{Path: "ab/new", Sequence: 2}}))
			updated, err := next.freeze(t.Context(), pages)
			testutil.FailErr(t, "freeze updated metadata", err)
			if got := directoryIndexPaths(t, original); !reflect.DeepEqual(got, paths) {
				t.Fatalf("original changed: %q", got)
			}
			want := []string{".", "ab", "ab/child", "ab/new", string([]byte{'z', 0xff})}
			if got := directoryIndexPaths(t, updated); !reflect.DeepEqual(got, want) {
				t.Fatalf("updated=%q, want %q", got, want)
			}
			old, _, err := original.Get(t.Context(), "ab")
			testutil.FailErr(t, "read original value", err)
			if old.observation.Sequence != 1 {
				t.Fatal("delta changed original metadata")
			}
			testutil.FailErr(t, "remove whole root", next.DeleteSubtree(t.Context(), "."))
			empty, err := next.freeze(t.Context(), pages)
			testutil.FailErr(t, "freeze empty tree", err)
			if empty.Len() != 0 || empty.root != 0 {
				t.Fatalf("empty index=%+v", empty)
			}
		})
	}
}

func TestStructuralDirectoryIndexCancellationAndRecordBound(t *testing.T) {
	workspace, err := newStructuralDirectoryWorkspace(t.TempDir(), structuralDirectoryIndex{})
	testutil.FailErr(t, "create bounded workspace", err)
	defer func() { _ = workspace.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := workspace.Get(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lookup=%v", err)
	}
	value := structuralDirectory{observation: DirectoryObservation{Path: "a", Failure: strings.Repeat("x", structuralDirectoryRecordLimit)}}
	if err := workspace.Set(t.Context(), "a", value); !errors.Is(err, pagedview.ErrFrameSize) {
		t.Fatalf("oversize record=%v", err)
	}
}
func directoryIndexPaths(t *testing.T, index structuralDirectoryIndex) []string {
	t.Helper()
	var paths []string
	testutil.FailErr(t, "visit metadata snapshot", index.Visit(t.Context(), func(record structuralDirectory) error { paths = append(paths, record.observation.Path); return nil }))
	return paths
}

func TestStructuralFinalizationSpillsItsWorkAndSkipsDeletedMetadata(t *testing.T) {
	store := checkpointTestStore(t)
	builder, err := newStructuralBuilder(store, nil)
	testutil.FailErr(t, "create spilled finalization builder", err)
	defer builder.close()
	builder.directories.memoryLimit = 1024
	const count = 300
	names := make([]string, count)
	for i := range names {
		names[i] = fmt.Sprintf("dir-%04d", i)
	}
	observePublicationFixture(t, builder, store, ".", names, true)
	for _, dir := range names {
		observePublicationFixture(t, builder, store, dir, []string{"file"}, false)
	}
	testutil.FailErr(t, "finalize spilled work", builder.finalize(t.Context()))
	if builder.directories.tx == nil || builder.directories.memory != nil {
		t.Fatal("finalization retained a full in-memory work set")
	}
	index, _, err := builder.children(t.Context(), ".")
	testutil.FailErr(t, "read finalized root", err)
	extent, err := index.Extent(t.Context())
	testutil.FailErr(t, "measure finalized root", err)
	if extent != count*2 {
		t.Fatalf("root extent=%d", extent)
	}
	testutil.FailErr(t, "delete observed descendant", builder.directories.DeleteSubtree(t.Context(), names[0]))
	builder.finalized = false
	testutil.FailErr(t, "finalize tombstoned work", builder.finalize(t.Context()))
	_, found, err := builder.directories.Get(t.Context(), names[0])
	testutil.FailErr(t, "read deleted metadata", err)
	if found {
		t.Fatal("finalization resurrected a tombstoned directory")
	}
}

func TestStructuralDirectoryCursorContinuesWhenMutationSpills(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			workspace, err := newStructuralDirectoryWorkspace(t.TempDir(), structuralDirectoryIndex{})
			testutil.FailErr(t, "create cursor workspace", err)
			defer func() { _ = workspace.Close() }()
			for _, key := range []string{"a", "b", "c"} {
				testutil.FailErr(t, "stage cursor entry", workspace.Set(t.Context(), key, structuralDirectory{observation: DirectoryObservation{Path: key}}))
			}
			workspace.memoryLimit = workspace.bytes
			cursor := directoryWorkCursor{workspace: workspace, reverse: reverse}
			var visited []string
			for {
				key, _, found, err := cursor.next(t.Context())
				testutil.FailErr(t, "advance directory cursor", err)
				if !found {
					break
				}
				visited = append(visited, key)
				if len(visited) == 1 {
					insert := "ab"
					if reverse {
						insert = "bc"
					}
					testutil.FailErr(t, "spill during cursor traversal", workspace.Set(t.Context(), insert, structuralDirectory{observation: DirectoryObservation{Path: insert}}))
					if workspace.tx == nil {
						t.Fatal("cursor fixture did not spill")
					}
				}
			}
			want := []string{"a", "ab", "b", "c"}
			if reverse {
				want = []string{"c", "bc", "b", "a"}
			}
			if !reflect.DeepEqual(visited, want) {
				t.Fatalf("visited=%v, want %v", visited, want)
			}
		})
	}
}
