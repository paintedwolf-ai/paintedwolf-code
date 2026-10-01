package sourcecatalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func walkFixture(paths map[string]bool) Snapshot {
	root := Root{ID: "root", Path: "/fixture"}
	entries := make(map[string]Entry, len(paths))
	for rel, isDir := range paths {
		mode := uint32(0o644)
		if isDir {
			mode = uint32(os.ModeDir | 0o755)
		}
		entries[rel] = Entry{RootID: root.ID, Path: rel, Parent: normalizeDir(path.Dir(rel)), Name: path.Base(rel),
			Depth: pathDepth(rel), IsDir: isDir, Mode: mode}
	}
	return snapshotFromEntries(root, entries)
}

func walkPaths(t *testing.T, s Snapshot, dir string, visit func(Entry) WalkStep) []string {
	t.Helper()
	var out []string
	err := s.Walk(t.Context(), "root", dir, func(entry Entry) WalkStep {
		out = append(out, entry.Path)
		return visit(entry)
	})
	testutil.FailErr(t, "walk "+dir, err)
	return out
}

func continueAll(Entry) WalkStep { return WalkContinue }

// A pruned subtree costs one visit: the walk's cost follows what it admits,
// not the size of the tree behind a skipped directory.
func TestSnapshotWalkSkippedSubtreeCostsOneVisit(t *testing.T) {
	paths := map[string]bool{"keep": true, "keep/a.go": false, "keep/b.go": false, "wide": true, "top.go": false}
	for dir := range 2000 {
		name := fmt.Sprintf("wide/d%04d", dir)
		paths[name] = true
		for file := range 10 {
			paths[fmt.Sprintf("%s/f%d", name, file)] = false
		}
	}
	s := walkFixture(paths)
	visited := walkPaths(t, s, ".", func(entry Entry) WalkStep {
		if entry.Path == "wide" {
			return WalkSkip
		}
		return WalkContinue
	})
	want := []string{"keep", "keep/a.go", "keep/b.go", "wide", "top.go"}
	if !slices.Equal(visited, want) {
		t.Fatalf("visited %v, want %v", visited, want)
	}
	if all := walkPaths(t, s, ".", continueAll); len(all) != len(paths) {
		t.Fatalf("unpruned walk visited %d entries, want %d", len(all), len(paths))
	}
}

func TestSnapshotWalkIsPreorderInListingOrder(t *testing.T) {
	s := walkFixture(map[string]bool{
		"b.go": false, "A": true, "A/z.go": false, "A/inner": true, "A/inner/x.go": false, "c": true,
	})
	visited := walkPaths(t, s, ".", continueAll)
	want := []string{"A", "A/inner", "A/inner/x.go", "A/z.go", "c", "b.go"}
	if !slices.Equal(visited, want) {
		t.Fatalf("visited %v, want %v", visited, want)
	}
	if below := walkPaths(t, s, "A", continueAll); !slices.Equal(below, []string{"A/inner", "A/inner/x.go", "A/z.go"}) {
		t.Fatalf("walk below A visited %v", below)
	}
	if missing := walkPaths(t, s, "absent", continueAll); len(missing) != 0 {
		t.Fatalf("walk below a missing directory visited %v", missing)
	}
}

func TestSnapshotWalkStopEndsTheWalk(t *testing.T) {
	s := walkFixture(map[string]bool{"a": true, "a/1": false, "a/2": false, "b": false})
	visited := walkPaths(t, s, ".", func(entry Entry) WalkStep {
		if entry.Path == "a/1" {
			return WalkStop
		}
		return WalkContinue
	})
	if !slices.Equal(visited, []string{"a", "a/1"}) {
		t.Fatalf("visited %v after stop", visited)
	}
}

func TestSnapshotWalkDoesNotEnterSymlinkedDirectories(t *testing.T) {
	s := walkFixture(map[string]bool{"link": true, "link/inside": false})
	for i := range s.Entries {
		if s.Entries[i].Path == "link" {
			s.Entries[i].IsSymlink = true
			s.Entries[i].TargetIsDir = true
		}
	}
	if visited := walkPaths(t, s, ".", continueAll); !slices.Equal(visited, []string{"link"}) {
		t.Fatalf("visited %v through a symlink", visited)
	}
}

func TestSnapshotWalkReturnsCancellation(t *testing.T) {
	s := walkFixture(map[string]bool{"a": false})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := s.Walk(ctx, "root", ".", continueAll)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("walk after cancel = %v, want context.Canceled", err)
	}
}
