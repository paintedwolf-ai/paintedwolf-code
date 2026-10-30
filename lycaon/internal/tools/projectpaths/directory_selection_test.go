//go:build darwin || linux

package projectpaths_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestDirectoryMembershipSelectionAndBoundsAreStable(t *testing.T) {
	tc, root := readSessionContext(t)
	testutil.FailErr(t, "create listing", os.Mkdir(filepath.Join(root, "list"), 0o700))
	for _, name := range []string{"a", "b", ".hidden", ".other"} {
		testutil.FailErr(t, "seed entry", os.WriteFile(filepath.Join(root, "list", name), nil, 0o600))
	}
	paths, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind invocation", err)
	bound, err := paths.Bind(t.Context(), "list", false)
	testutil.FailErr(t, "bind listing", err)
	for _, hidden := range []bool{false, true} {
		dir, err := bound.OpenDir()
		testutil.FailErr(t, "open listing", err)
		want := []string{"a", "b"}
		if hidden {
			want = []string{".hidden", ".other", "a", "b"}
		}
		names, err := dir.Names(projectpaths.DirectoryNames{Maximum: len(want), IncludeHidden: hidden})
		testutil.FailErr(t, "select membership", err)
		if !slices.Equal(names, want) {
			t.Fatalf("hidden=%v names=%v want=%v", hidden, names, want)
		}
		if _, err := dir.Names(projectpaths.DirectoryNames{IncludeHidden: !hidden}); err == nil {
			t.Fatal("changed selection reused an incomplete snapshot")
		}
		_, err = dir.Names(projectpaths.DirectoryNames{Maximum: 1, IncludeHidden: hidden})
		var exceeded *projectpaths.DirectoryEntriesLimitError
		if !errors.As(err, &exceeded) || exceeded.Entries != 2 {
			t.Fatalf("cached limit=%v", err)
		}
		testutil.FailErr(t, "close listing", dir.Close())
	}
}

func TestHeldNameAdmissionDoesNotReadMetadataOrFollowLinks(t *testing.T) {
	tc, root := readSessionContext(t)
	testutil.FailErr(t, "create listing", os.Mkdir(filepath.Join(root, "list"), 0o700))
	testutil.FailErr(t, "create link", os.Symlink("/outside/absent", filepath.Join(root, "list", "visible")))
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: toolprofiles.DefaultToolProfileID, Tools: map[string]bool{"read": true}, ReadGlobs: []string{"list", "list/visible"}}})
	paths, err := projectpaths.NewProgramPaths(t.Context(), boundary, tc, "list")
	testutil.FailErr(t, "bind invocation", err)
	bound, err := paths.Bind(t.Context(), ".", false)
	testutil.FailErr(t, "bind listing", err)
	dir, err := bound.OpenDir()
	testutil.FailErr(t, "hold listing", err)
	child, err := dir.Child("visible")
	testutil.FailErr(t, "admit link name", err)
	if child.Resolved.ScopeRel != "list/visible" {
		t.Fatalf("child=%+v", child.Resolved)
	}
	testutil.FailErr(t, "remove entry", os.Remove(filepath.Join(root, "list", "visible")))
	if _, err := dir.Child("visible"); err != nil {
		t.Fatalf("name admission required metadata: %v", err)
	}
	for _, name := range []string{"denied", "../visible", "", ".", "..", "other/visible", "nul\x00name"} {
		if _, err := dir.Child(name); err == nil {
			t.Fatalf("admitted invalid or unauthorized name %q", name)
		}
	}
	testutil.FailErr(t, "close listing", dir.Close())
	if _, err := dir.Child("visible"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed directory admitted child: %v", err)
	}
}
