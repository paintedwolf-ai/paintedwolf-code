package mutation

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

func TestRecursiveNewDestinationKeepsEveryFileAttributed(t *testing.T) {
	h := newHarness(t, map[string]string{"tree/a": "first", "tree/dir/b": "second", "tree/empty/": ""})
	r := h.run("", "cp", "-r", "tree", "copy")
	if r.err != nil || r.result.Status != 0 {
		t.Fatalf("copy result=%+v error=%v", r.result, r.err)
	}
	paths, err := h.tc.Source.History.Authorship.SessionAuthoredPaths(t.Context(), h.project.ID, "s1", "r1")
	testutil.FailErr(t, "read attributed copies", err)
	for _, path := range []string{"copy/a", "copy/dir/b"} {
		found := false
		for _, observed := range paths {
			if observed == path {
				found = true
			}
		}
		if !found {
			t.Fatalf("copy has no attributed effect for %s: %+v", path, paths)
		}
	}
	info, err := os.Stat(h.path("copy/empty"))
	testutil.FailErr(t, "copied empty directory", err)
	if !info.IsDir() {
		t.Fatal("empty directory was lost")
	}
}

func TestRecursiveCopyRechecksGrowthAfterPreflight(t *testing.T) {
	h := newHarness(t, map[string]string{"tree/a": "aa", "tree/b": "bb"})
	h.owners.Operations.Copy.Caps = native.RecursiveCaps{Files: 2, Bytes: 8}
	changed := false
	h.tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		for _, change := range changes {
			if changed || !strings.HasSuffix(change.Path, "/copy") {
				continue
			}
			changed = true
			testutil.FailErr(t, "grow source after preflight", os.WriteFile(h.path("tree/a"), []byte(strings.Repeat("a", 9)), 0o644))
			testutil.FailErr(t, "grow other source after preflight", os.WriteFile(h.path("tree/b"), []byte(strings.Repeat("b", 9)), 0o644))
		}
		return nil
	}
	r := h.run("", "cp", "-r", "tree", "copy")
	if !changed || r.err == nil {
		t.Fatalf("growth escaped quota: changed=%v result=%+v error=%v", changed, r.result, r.err)
	}
	for _, path := range []string{"copy/a", "copy/b"} {
		if !h.absent(path) {
			t.Fatalf("oversize file published at %s", path)
		}
	}
}

func TestCopyNewFilesKeepsSourcePermissionsAndExistingFilesKeepTheirMode(t *testing.T) {
	h := newHarness(t, map[string]string{"tree/tool": "executable", "existing": "old"})
	testutil.FailErr(t, "set executable source mode", os.Chmod(h.path("tree/tool"), 0o750))
	testutil.FailErr(t, "set existing destination mode", os.Chmod(h.path("existing"), 0o600))
	for _, args := range [][]string{{"cp", "-r", "tree", "copy"}, {"cp", "tree/tool", "existing"}} {
		if r := h.run("", args...); r.err != nil || r.result.Status != 0 {
			t.Fatalf("copy mode: result=%+v error=%v", r.result, r.err)
		}
	}
	for path, want := range map[string]os.FileMode{"copy/tool": 0o750, "existing": 0o600} {
		info, err := os.Stat(h.path(path))
		testutil.FailErr(t, "stat copy", err)
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode=%04o; want %04o", path, got, want)
		}
	}
}

func TestRecursiveCopySharesLimitsAcrossAllSources(t *testing.T) {
	for _, sources := range []struct {
		name, first, second, firstResult, secondResult string
	}{
		{"trees", "left", "right", "dest/left/a", "dest/right/b"},
		{"file_then_tree", "first", "right", "dest/first", "dest/right/b"},
		{"tree_then_file", "left", "second", "dest/left/a", "dest/second"},
		{"files", "first", "second", "dest/first", "dest/second"},
	} {
		for _, bound := range []struct {
			name string
			caps native.RecursiveCaps
			fail bool
		}{
			{"at", native.RecursiveCaps{Files: 2, Bytes: 4}, false},
			{"bytes_over", native.RecursiveCaps{Files: 2, Bytes: 3}, true},
			{"files_over", native.RecursiveCaps{Files: 1, Bytes: 4}, true},
		} {
			t.Run(sources.name+"/"+bound.name, func(t *testing.T) {
				h := newHarness(t, map[string]string{"left/a": "aa", "right/b": "bb", "first": "aa", "second": "bb", "dest/": ""})
				h.owners.Operations.Copy.Caps = bound.caps
				r := h.run("", "cp", "-r", sources.first, sources.second, "dest")
				if bound.fail {
					if r.err == nil || !h.absent(sources.secondResult) {
						t.Fatalf("aggregate cap escaped: result=%+v error=%v", r.result, r.err)
					}
				} else if r.err != nil || r.result.Status != 0 || h.read(sources.secondResult) != "bb" {
					t.Fatalf("copy at aggregate cap: result=%+v error=%v", r.result, r.err)
				}
				if got := h.read(sources.firstResult); got != "aa" {
					t.Fatalf("earlier completed copy changed: %q", got)
				}
			})
		}
	}
}

func TestRecursiveCopyKeepsPublishedBytesChargedAfterMetadataFailure(t *testing.T) {
	h := newHarness(t, map[string]string{"left/a": "aa", "right/b": "bb", "dest/": ""})
	h.owners.Operations.Copy.Caps = native.RecursiveCaps{Files: 2, Bytes: 3}
	failedMetadata := false
	h.tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		for _, change := range changes {
			if change.Path == h.path("dest/left/a") {
				if _, err := os.Stat(change.Path); err == nil {
					failedMetadata = true
					return os.ErrNotExist
				}
			}
		}
		return nil
	}
	r := h.run("", "cp", "-r", "-p", "left", "right", "dest")
	if !failedMetadata || r.err == nil || h.read("dest/left/a") != "aa" || !h.absent("dest/right/b") {
		t.Fatalf("metadata failure reset published-byte use: injected=%v result=%+v error=%v", failedMetadata, r.result, r.err)
	}
}
