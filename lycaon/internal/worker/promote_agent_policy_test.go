package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

// Promotion never lands the project overlay: approvals, MCP servers, rules and
// the rest of the policy governing a worker stay on the primary as they were,
// even when a caller names them, while ordinary files land.
func TestPromoteOverlayNeverLandsTheProjectOverlay(t *testing.T) {
	ctx := context.Background()
	primary := t.TempDir()
	overlay := t.TempDir()
	baseline := workspaceBaselinePath(t, primary)
	var overlayFiles []string
	for _, location := range protectedpath.AgentPolicyLocations() {
		switch {
		case location.File != "":
			overlayFiles = append(overlayFiles, location.File)
		case location.Dir != "" && settingsoverlay.DirName() == filepath.ToSlash(filepath.Dir(location.Dir)):
			overlayFiles = append(overlayFiles, location.Dir+"/changed.md")
		}
	}
	if len(overlayFiles) == 0 {
		t.Fatal("no project overlay locations to promote")
	}
	for _, rel := range append([]string{"src/landed.go"}, overlayFiles...) {
		full := filepath.Join(overlay, filepath.FromSlash(rel))
		testutil.FailErr(t, "MkdirAll", os.MkdirAll(filepath.Dir(full), 0o755))
		testutil.FailErr(t, "WriteFile", os.WriteFile(full, []byte("changed by the worker\n"), 0o644))
	}
	scope := api.TaskScope{Mode: api.TaskScopeModeWrite}
	task := &api.WorkerTask{
		ID: "job-policy", ParentSessionID: "parent-1", ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: primary, WorkspaceRoot: overlay, WorkspaceBaselinePath: baseline,
		MergeStatus: api.WorkerMergeStatusPending, AgentType: "implementer", Scope: &scope, Status: api.WorkerStatusComplete,
	}
	svc := &worker.MergeService{Queue: &mergeQueueStub{task: task}, Store: &mergeStoreStub{}, Reject: mergeRejectFmt(t)}

	var named []api.WorkerPromoteResolution
	for _, rel := range overlayFiles {
		named = append(named, api.WorkerPromoteResolution{Path: rel, Action: api.WorkerPromoteResolutionActionKeepTheirs})
	}
	if _, err := svc.PromoteOverlay(ctx, "parent-1", task.ID, api.PromoteOverlayInput{Resolutions: named}); err == nil {
		t.Log("resolutions naming overlay paths were accepted; the files must still not land")
	}
	if _, err := svc.PromoteOverlay(ctx, "parent-1", task.ID, api.PromoteOverlayInput{}); err != nil {
		t.Logf("promote: %v", err)
	}
	for _, rel := range overlayFiles {
		if _, err := os.Lstat(filepath.Join(primary, filepath.FromSlash(rel))); err == nil {
			t.Errorf("%s landed on the primary", rel)
		}
	}
	if _, err := os.Lstat(filepath.Join(primary, "src", "landed.go")); err != nil {
		t.Errorf("ordinary file did not land: %v", err)
	}
}
