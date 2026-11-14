package workspacebaseline_test

import (
	"errors"
	"path/filepath"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

func twoRoots(t *testing.T) []projectroot.RootRef {
	t.Helper()
	return []projectroot.RootRef{{ID: "p", Label: "app", Path: t.TempDir(), IsPrimary: true}, {ID: "s", Label: "shared", Path: t.TempDir()}}
}

// A manifest path maps onto its root's project file, and every path that cannot name one is refused.
func TestCanonicalPathMapsManifestPathsOntoTheirRoot(t *testing.T) {
	roots := twoRoots(t)
	for path, want := range map[string]string{
		"src/a.go":      filepath.Join(roots[0].Path, "src", "a.go"),
		"@shared/b.txt": filepath.Join(roots[1].Path, "b.txt"),
		"@SHARED/c.txt": filepath.Join(roots[1].Path, "c.txt"),
	} {
		got, err := workspacebaseline.CanonicalPath(roots, path)
		testutil.FailErr(t, "canonical "+path, err)
		if got != want {
			t.Fatalf("%s maps to %s, want %s", path, got, want)
		}
	}
	noPrimary := []projectroot.RootRef{{ID: "a", Label: "a", Path: t.TempDir()}, {ID: "b", Label: "b", Path: t.TempDir()}}
	for name, tc := range map[string]struct {
		roots []projectroot.RootRef
		path  string
	}{
		"no roots":         {nil, "a.go"},
		"unqualified root": {roots, "@shared"},
		"unknown root":     {roots, "@missing/a.go"},
		"parent traversal": {roots, "../outside.go"},
		"empty in a root":  {roots, "@shared/"},
		"no primary root":  {noPrimary, "a.go"},
	} {
		if _, err := workspacebaseline.CanonicalPath(tc.roots, tc.path); err == nil {
			t.Fatalf("%s: %q mapped", name, tc.path)
		}
		if _, err := workspacebaseline.LocateInBranch(tc.roots, t.TempDir(), tc.path); err == nil {
			t.Fatalf("%s: %q located in a branch", name, tc.path)
		}
	}
}

// A visitor's error stops the walk over changes and is returned as is.
func TestEachChangeStopsOnTheVisitorsError(t *testing.T) {
	branch, reader := sealedBranch(t, map[string][]byte{"a.txt": []byte("a\n"), "b.txt": []byte("b\n")})
	rewrite(t, filepath.Join(branch, "a.txt"), "changed a\n")
	rewrite(t, filepath.Join(branch, "b.txt"), "changed b\n")
	stop := errors.New("stop")
	visits := 0
	err := reader.EachChange(t.Context(), nil, branch, func(string) error {
		visits++
		return stop
	})
	if !errors.Is(err, stop) || visits != 1 {
		t.Fatalf("err=%v after %d visits", err, visits)
	}
}

// A sealed overlay's changes come back with their roots, their baseline state and readable bodies.
func TestEachOverlayChangeVisitsEverySealedPathWithItsBase(t *testing.T) {
	root, branch := t.TempDir(), t.TempDir()
	roots := twoRoots(t)
	dirs := map[string]string{}
	for _, r := range roots {
		dir, err := projectroot.BranchDirForID(r.ID)
		testutil.FailErr(t, "root directory", err)
		dirs[r.ID] = dir
		writeFile(t, filepath.Join(branch, dir, "file.txt"), []byte("base\n"), 0o644)
	}
	store := workspacebaseline.New(nil, sourceblob.New(filepath.Join(root, "source-content")), filepath.Join(root, "manifests"))
	baseline, err := store.Capture(t.Context(), "job", workspacebaseline.Branch(roots, branch))
	testutil.FailErr(t, "capture baseline", err)
	writeFile(t, filepath.Join(branch, dirs["s"], "file.txt"), []byte("changed\n"), 0o644)
	writeFile(t, filepath.Join(branch, dirs["p"], "added.txt"), []byte("added\n"), 0o644)
	captured, err := store.CaptureOverlay(t.Context(), "job", baseline, roots, branch)
	testutil.FailErr(t, "capture overlay", err)

	type seen struct {
		root, rel, body string
		baseExists      bool
	}
	var got []seen
	testutil.FailErr(t, "each overlay change", workspacebaseline.EachOverlayChange(t.Context(), baseline, captured.Path, roots, func(c workspacebaseline.OverlayChange) error {
		body, err := c.Body(c.Final.SHA256)
		got = append(got, seen{c.RootID, c.Rel, string(body), c.BaseExists})
		return err
	}))
	sort.Slice(got, func(i, j int) bool { return got[i].root+got[i].rel < got[j].root+got[j].rel })
	want := []seen{{"p", "added.txt", "added\n", false}, {"s", "file.txt", "changed\n", true}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("overlay changes %+v, want %+v", got, want)
	}

	if err := workspacebaseline.EachOverlayChange(t.Context(), baseline, captured.Path, roots[:0:0], func(workspacebaseline.OverlayChange) error { return nil }); err == nil {
		t.Fatal("an overlay read without its roots must fail")
	}
	if err := workspacebaseline.EachOverlayChange(t.Context(), filepath.Join(root, "missing"), captured.Path, roots, func(workspacebaseline.OverlayChange) error { return nil }); err == nil {
		t.Fatal("a missing baseline must fail")
	}
	if err := workspacebaseline.EachOverlayChange(t.Context(), baseline, filepath.Join(root, "missing"), roots, func(workspacebaseline.OverlayChange) error { return nil }); err == nil {
		t.Fatal("a missing overlay must fail")
	}
}
