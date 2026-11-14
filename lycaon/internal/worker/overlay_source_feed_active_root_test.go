package worker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Promotion pre-images resolve baseline spellings to their structured root
// identity; the task's active root never re-keys them.
func TestSnapshotPromoteTargetsResolveBaselineSpellings(t *testing.T) {
	base := t.TempDir()
	primary, secondary := filepath.Join(base, "app"), filepath.Join(base, "den")
	testutil.FailErr(t, "mkdir roots", os.MkdirAll(primary, 0o750))
	testutil.FailErr(t, "mkdir roots", os.MkdirAll(secondary, 0o750))
	testutil.FailErr(t, "seed primary", os.WriteFile(filepath.Join(primary, "notes.md"), []byte("primary\n"), 0o600))
	testutil.FailErr(t, "seed secondary", os.WriteFile(filepath.Join(secondary, "readme.md"), []byte("docs\n"), 0o600))

	roots := []projectroot.RootRef{
		{ID: "primary-id", Label: "app", Path: primary, IsPrimary: true},
		{ID: "secondary-id", Label: "den", Path: secondary},
	}
	task := &api.WorkerTask{ProjectID: "proj", WorkspaceRootID: "secondary-id"}

	targets, err := (&MergeService{}).snapshotPromoteTargets(task, roots, []string{"notes.md", "@den/readme.md", "@den/gone.md"})
	testutil.FailErr(t, "snapshot promote targets", err)
	if len(targets) != 3 {
		t.Fatalf("targets = %+v", targets)
	}

	primaryTarget := targets["notes.md"]
	if primaryTarget.rootID != "primary-id" || primaryTarget.relPath != "notes.md" || primaryTarget.abs != filepath.Join(primary, "notes.md") {
		t.Fatalf("bare spelling = %+v", primaryTarget)
	}
	if !primaryTarget.existed || string(primaryTarget.before) != "primary\n" || primaryTarget.beforeSHA == "" {
		t.Fatalf("bare pre-image = %+v", primaryTarget)
	}

	secondaryTarget := targets["@den/readme.md"]
	if secondaryTarget.rootID != "secondary-id" || secondaryTarget.relPath != "readme.md" || secondaryTarget.abs != filepath.Join(secondary, "readme.md") {
		t.Fatalf("labeled spelling = %+v", secondaryTarget)
	}
	if !secondaryTarget.existed || string(secondaryTarget.before) != "docs\n" {
		t.Fatalf("labeled pre-image = %+v", secondaryTarget)
	}

	missing := targets["@den/gone.md"]
	if missing.rootID != "secondary-id" || missing.existed || missing.before != nil {
		t.Fatalf("missing target = %+v", missing)
	}
}
