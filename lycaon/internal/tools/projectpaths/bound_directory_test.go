//go:build darwin || linux

package projectpaths_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestHeldDirectoryReadsMembershipOnlyOnDemand(t *testing.T) {
	tc, root := readSessionContext(t)
	paths, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind invocation", err)
	bound, err := paths.Bind(t.Context(), ".", false)
	testutil.FailErr(t, "bind directory", err)
	shared, err := fseffect.OpenReadRoot(root)
	testutil.FailErr(t, "open shared root", err)
	defer func() { testutil.FailErr(t, "close shared root", shared.Close()) }()
	directory, err := bound.OpenDirWithRoot(shared)
	testutil.FailErr(t, "hold directory", err)
	testutil.FailErr(t, "create after opening", os.WriteFile(filepath.Join(root, "late"), []byte("late"), 0o600))
	_, stat, err := directory.StatChild("late")
	testutil.FailErr(t, "stat without listing", err)
	if stat.Size != 4 {
		t.Fatalf("held child=%+v", stat)
	}
	first, err := directory.Names(projectpaths.DirectoryNames{Maximum: 0, IncludeHidden: true})
	testutil.FailErr(t, "read names on demand", err)
	if !slices.Contains(first, "late") {
		t.Fatalf("opening consumed membership: %v", first)
	}
	testutil.FailErr(t, "create after listing", os.WriteFile(filepath.Join(root, "later"), []byte("later"), 0o600))
	second, err := directory.Names(projectpaths.DirectoryNames{Maximum: 0, IncludeHidden: true})
	testutil.FailErr(t, "reuse held membership", err)
	if !slices.Equal(first, second) || slices.Contains(second, "later") {
		t.Fatalf("held membership changed: %v -> %v", first, second)
	}
	testutil.FailErr(t, "close directory", directory.Close())
	file, err := shared.OpenExact("late")
	testutil.FailErr(t, "shared root survives directory", err)
	testutil.FailErr(t, "close retained file", file.Close())
}

func TestHeldDirectoryBoundsOnlyVisibleNames(t *testing.T) {
	tc, root := readSessionContext(t)
	testutil.FailErr(t, "create recovery tree", os.MkdirAll(filepath.Join(root, ".paintedwolf", "recovery"), 0o700))
	paths, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind invocation", err)
	bound, err := paths.Bind(t.Context(), ".", false)
	testutil.FailErr(t, "bind directory", err)
	directory, err := bound.OpenDir()
	testutil.FailErr(t, "open bounded directory", err)
	defer func() { testutil.FailErr(t, "close bounded directory", directory.Close()) }()
	names, err := directory.Names(projectpaths.DirectoryNames{Maximum: 1, IncludeHidden: true})
	var exceeded *projectpaths.DirectoryEntriesLimitError
	if !errors.As(err, &exceeded) || exceeded.Entries != 2 || len(names) != 0 {
		t.Fatalf("bounded names=%v error=%v", names, err)
	}
	if _, err := directory.Names(projectpaths.DirectoryNames{Maximum: 0, IncludeHidden: true}); !errors.As(err, &exceeded) {
		t.Fatalf("partial listing became complete: %v", err)
	}
}

func TestClosedDirectoryCannotPublishAnEmptyNamespace(t *testing.T) {
	tc, _ := readSessionContext(t)
	paths, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind invocation", err)
	bound, err := paths.Bind(t.Context(), ".", false)
	testutil.FailErr(t, "bind directory", err)
	directory, err := bound.OpenDir()
	testutil.FailErr(t, "hold directory", err)
	testutil.FailErr(t, "close directory", directory.Close())
	var first error
	for range 2 {
		names, err := directory.Names(projectpaths.DirectoryNames{Maximum: 0, IncludeHidden: true})
		if err == nil || len(names) != 0 {
			t.Fatalf("closed namespace=%v error=%v", names, err)
		}
		if first != nil && !errors.Is(err, first) {
			t.Fatal("closed directory discarded its failed observation")
		}
		first = err
	}
}
