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
