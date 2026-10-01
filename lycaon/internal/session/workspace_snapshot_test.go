package session_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

func TestWorkspaceSnapshotDetectsFileChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "src", "a.go")
	testutil.FailErr(t, "create directory", os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "write file", os.WriteFile(path, []byte("v1"), 0o644))
	baseline, err := session.SnapshotWorkspace(dir)
	testutil.FailErr(t, "snapshot workspace", err)
	if len(baseline) != 1 {
		t.Fatalf("baseline = %+v", baseline)
	}
	testutil.FailErr(t, "change file", os.WriteFile(path, []byte("v2-longer"), 0o644))
	changed := session.DiffWorkspaceSnapshot(dir, baseline)
	if len(changed) != 1 || changed[0] != "src/a.go" {
		t.Fatalf("changed = %v", changed)
	}
}

func TestWorkspaceSnapshotWalksTree(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "create directory", os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	testutil.FailErr(t, "write root file", os.WriteFile(filepath.Join(dir, "index.html"), []byte("root"), 0o644))
	testutil.FailErr(t, "write nested file", os.WriteFile(filepath.Join(dir, "src", "game.js"), []byte("game"), 0o644))
	baseline, err := session.SnapshotWorkspace(dir)
	testutil.FailErr(t, "snapshot workspace", err)
	for _, rel := range []string{"index.html", "src/game.js"} {
		if _, ok := baseline[rel]; !ok {
			t.Fatalf("baseline missing %s: %+v", rel, baseline)
		}
	}
}

func TestEmptyWorkspaceBaselineIsValidAndDetectsLaterCreation(t *testing.T) {
	dir := t.TempDir()
	ref := testbaseline.Capture(t, dir)
	baseline, err := workspacebaseline.Open(t.Context(), ref, workspacebaseline.ContentStore(ref))
	testutil.FailErr(t, "open empty baseline", err)
	defer func() { _ = baseline.Close() }()
	testutil.FailErr(t, "write greenfield file", os.WriteFile(filepath.Join(dir, "new.go"), []byte("package new\n"), 0o644))
	changed, err := baseline.Changes(t.Context(), nil, dir)
	testutil.FailErr(t, "compare empty baseline", err)
	if len(changed) != 1 || changed[0] != "new.go" {
		t.Fatalf("changed = %v", changed)
	}
}

func TestWorkspaceSnapshotDetectsDeletion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gone.go")
	testutil.FailErr(t, "write file", os.WriteFile(path, []byte("package gone\n"), 0o644))
	baseline, err := session.SnapshotWorkspace(dir)
	testutil.FailErr(t, "snapshot workspace", err)
	testutil.FailErr(t, "remove file", os.Remove(path))
	changed := session.DiffWorkspaceSnapshot(dir, baseline)
	if len(changed) != 1 || changed[0] != "gone.go" {
		t.Fatalf("changed = %v", changed)
	}
}
