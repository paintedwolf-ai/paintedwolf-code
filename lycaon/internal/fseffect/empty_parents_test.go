package fseffect

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEmptyParentPruneStopsAtScopeAndRetainsPopulatedParent(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "create source parents", os.MkdirAll(filepath.Join(root, "stop", "deep"), 0700))
	file := filepath.Join(root, "stop", "deep", "file")
	testutil.FailErr(t, "seed leaf", os.WriteFile(file, []byte("saved"), 0600))
	plan, err := PrepareEmptyParents(Location{Root: root, Rel: "stop/deep/file"}, "stop")
	testutil.FailErr(t, "prepare cleanup", err)
	testutil.FailErr(t, "remove leaf", os.Remove(file))
	testutil.FailErr(t, "prune source parents", PruneEmptyParents(t.Context(), root, plan))
	if _, err := os.Stat(filepath.Join(root, "stop", "deep")); !os.IsNotExist(err) {
		t.Fatal("empty source parent remains")
	}
	if _, err := os.Stat(filepath.Join(root, "stop")); err != nil {
		t.Fatal("cleanup removed stop directory")
	}
	testutil.FailErr(t, "idempotent cleanup", PruneEmptyParents(t.Context(), root, plan))
}

func TestEmptyParentPrunePreservesConcurrentEntriesAndReplacements(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "populated", true: "replaced"}[replacement], func(t *testing.T) {
			root := t.TempDir()
			parent := filepath.Join(root, "source")
			testutil.FailErr(t, "source parent", os.Mkdir(parent, 0755))
			file := filepath.Join(parent, "leaf")
			testutil.FailErr(t, "leaf", os.WriteFile(file, []byte("saved"), 0644))
			plan, err := PrepareEmptyParents(Location{Root: root, Rel: "source/leaf"}, ".")
			testutil.FailErr(t, "prepare", err)
			testutil.FailErr(t, "remove leaf", os.Remove(file))
			if replacement {
				testutil.FailErr(t, "move original parent", os.Rename(parent, filepath.Join(root, "retained-parent")))
				testutil.FailErr(t, "replacement parent", os.Mkdir(parent, 0755))
			} else {
				testutil.FailErr(t, "concurrent file", os.WriteFile(filepath.Join(parent, "new"), []byte("retained"), 0644))
			}
			testutil.FailErr(t, "guarded cleanup", PruneEmptyParents(t.Context(), root, plan))
			if _, err := os.Stat(parent); err != nil {
				t.Fatal("unrelated parent was removed")
			}
		})
	}
}

func TestEmptyParentPlanRefusesEscapingAndNonAncestorStops(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "parent", os.Mkdir(filepath.Join(root, "source"), 0755))
	for _, stop := range []string{"../outside", "source/child", "/absolute"} {
		if _, err := PrepareEmptyParents(Location{Root: root, Rel: "source/leaf"}, stop); err == nil {
			t.Fatalf("accepted stop %q", stop)
		}
	}
}
