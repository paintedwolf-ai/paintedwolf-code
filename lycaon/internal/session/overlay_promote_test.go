package session_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOverlayChangedPathsHonorsCallerCancellation(t *testing.T) {
	root := t.TempDir()
	baseline := testbaseline.Capture(t, root)
	testutil.FailErr(t, "write change", os.WriteFile(filepath.Join(root, "changed.go"), []byte("changed\n"), 0o600))
	task := &api.WorkerTask{
		WorkspaceRoot: root, WorkspaceBaselinePath: baseline,
		Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite},
	}
	if paths := session.OverlayWorkspaceChangedPaths(t.Context(), task, nil); len(paths) != 1 {
		t.Fatalf("live inspection paths=%v", paths)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if paths := session.OverlayWorkspaceChangedPaths(ctx, task, nil); len(paths) != 0 {
		t.Fatalf("canceled inspection continued: %v", paths)
	}
	if !session.OverlayAwaitingPromote(ctx, task) {
		t.Fatal("canceled inspection treated unresolved work as clean")
	}
}

func TestOverlayChangedPathsIncludesChangesOutsideSuggestedScope(t *testing.T) {
	primary := t.TempDir()
	overlay := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-a")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(overlay, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "a.go"), []byte("branch\n"), 0o644))
	testutil.FailErr(t, "WriteFile extra", os.WriteFile(filepath.Join(overlay, "b.go"), []byte("extra\n"), 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.go"}}
	baseline := testbaseline.Capture(t, primary)

	task := &api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary,
		WorkspaceRoot:         overlay,
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
	}

	paths := session.OverlayWorkspaceChangedPaths(t.Context(), task, nil)
	if len(paths) != 2 || paths[0] != "a.go" || paths[1] != "b.go" {
		t.Fatalf("OverlayWorkspaceChangedPaths = %v want [a.go b.go]", paths)
	}
	if !session.OverlayAwaitingPromote(t.Context(), task) {
		t.Fatal("expected overlay awaiting promote")
	}
}

func TestOverlayAwaitingPromoteWithAbsolutePathSuggestion(t *testing.T) {
	primary := t.TempDir()
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(primary, "index.html"), []byte("v1"), 0o644))

	overlay := t.TempDir()
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "index.html"), []byte("v2-rewritten"), 0o644))
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(filepath.Join(overlay, "src"), 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "src", "game.js"), []byte("game"), 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{primary}}
	baseline := testbaseline.Capture(t, primary)
	if len(baseline) == 0 {
		t.Fatalf("baseline = %+v", baseline)
	}

	task := &api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary,
		WorkspaceRoot:         overlay,
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
	}
	if !session.OverlayAwaitingPromote(t.Context(), task) {
		t.Fatal("whole-repo absolute scope overlay must await promote, not abort")
	}
	paths := session.OverlayWorkspaceChangedPaths(t.Context(), task, nil)
	if len(paths) != 2 {
		t.Fatalf("changed = %v want index.html + src/game.js", paths)
	}
}

func TestOverlayDivergencePathsSafetyNet(t *testing.T) {
	primary := t.TempDir()
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(primary, "index.html"), []byte("v1"), 0o644))

	overlay := t.TempDir()
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "index.html"), []byte("v2-rewritten"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "extra.js"), []byte("new"), 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"untouched.txt"}}
	task := &api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary,
		WorkspaceRoot: overlay,
		Scope:         &scope,
	}
	div := session.OverlayWorkspaceDivergencePaths(task, nil)
	if len(div) != 2 {
		t.Fatalf("divergence = %v want index.html + extra.js", div)
	}
}

func TestOverlayAwaitingPromoteFalseWithoutDiff(t *testing.T) {
	primary := t.TempDir()
	overlay := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-b")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(overlay, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "a.go"), []byte("same\n"), 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.go"}}
	baseline := testbaseline.Capture(t, overlay)

	task := &api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary,
		WorkspaceRoot:         overlay,
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
	}
	if session.OverlayAwaitingPromote(t.Context(), task) {
		t.Fatal("expected no promote when overlay matches baseline")
	}
}

func TestOverlayWorkspaceAvailableRejectsSymlinkRoot(t *testing.T) {
	branch := filepath.Join(t.TempDir(), "branch")
	if err := os.Symlink(t.TempDir(), branch); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if session.OverlayWorkspaceAvailable(&api.WorkerTask{WorkspaceRoot: branch}) {
		t.Fatal("OverlayWorkspaceAvailable accepted a symlink root")
	}
}

func TestOverlayAwaitingPromoteFalseWithoutChanges(t *testing.T) {
	primary := t.TempDir()
	overlay := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-absent")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(overlay, 0o755))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"test-chess.js"}}
	baseline := testbaseline.Capture(t, primary)

	task := &api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary,
		WorkspaceRoot:         overlay,
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
	}
	if session.OverlayAwaitingPromote(t.Context(), task) {
		t.Fatal("expected no promote without changes")
	}
}

func TestOverlayAwaitingPromoteFalseWhenWorkspaceGone(t *testing.T) {
	primary := t.TempDir()
	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.go"}}
	baseline := testbaseline.Capture(t, primary)

	task := &api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary,
		WorkspaceRoot:         filepath.Join(primary, settingsoverlay.DirName(), "overlays", "gone"),
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
	}
	if session.OverlayAwaitingPromote(t.Context(), task) {
		t.Fatal("expected false when branch directory is missing")
	}
}

func TestResolveWorkerSummaryStatus(t *testing.T) {
	primary := t.TempDir()
	overlay := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-c")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(overlay, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "a.go"), []byte("new\n"), 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.go"}}
	baseline := testbaseline.Capture(t, primary)

	task := &api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary,
		WorkspaceRoot:         overlay,
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
	}
	got := session.ResolveWorkerSummaryStatus(t.Context(), api.WorkerSummaryStatusComplete, task)
	if got != api.WorkerSummaryStatusOpen {
		t.Fatalf("eligible status = %q want open", got)
	}
	got = session.ResolveWorkerSummaryStatus(t.Context(), api.WorkerSummaryStatusComplete, task)
	if got != api.WorkerSummaryStatusOpen {
		t.Fatalf("unmet status = %q want partial", got)
	}
}
