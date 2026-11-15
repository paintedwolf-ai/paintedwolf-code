package worker

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
)

func assertRecoveredSourceConflict(t *testing.T, service *MergeService, task *api.WorkerTask, source, checkpointID, commit string) {
	t.Helper()
	_, err := service.PromoteOverlay(t.Context(), "", task.ID, api.PromoteOverlayInput{})
	var rejection *toolrejection.ToolReject
	if !errors.As(err, &rejection) || rejection.Code != OverlayPromoteConflictCode {
		t.Fatalf("changed source did not produce structured promotion conflict: %v", err)
	}
	assertFileHolds(t, filepath.Join(source, "main.go"), "package external\n")
	assertFileHolds(t, filepath.Join(task.WorkspaceRoot, "main.go"), "package worker\n")
	if got := strings.TrimSpace(gittest.Run(t, task.WorkspaceRoot, "rev-parse", "HEAD")); got != commit {
		t.Fatalf("rejected promotion changed worker Git history: %s", got)
	}
	retained, ok := service.Queue.Get(task.ID)
	if !ok {
		t.Fatal("rejected promotion removed worker")
	}
	store, manifest, published, err := session.OpenTaskCheckpointStore(retained)
	testutil.FailErr(t, "reopen rejected checkpoint", err)
	if !published || manifest.CheckpointID != checkpointID || len(manifest.Entries) != 1 {
		t.Fatalf("rejected promotion changed retained checkpoint: %+v", manifest)
	}
	base, err := store.ReadBlob(manifest.Entries[0].Base.SHA256)
	testutil.FailErr(t, "read rejected checkpoint base", err)
	if string(base) != "package main\n" {
		t.Fatalf("external source edit replaced exact base: %q", base)
	}
	// Restore only this fixture's source, then the caller retries the same checkpoint.
	testutil.FailErr(t, "resolve fixture source conflict", os.WriteFile(filepath.Join(source, "main.go"), base, 0o644))
	_, err = service.PromoteOverlay(t.Context(), "", task.ID, api.PromoteOverlayInput{})
	if !errors.Is(err, checkpoint.ErrStalePreviewTargetChanged) {
		t.Fatalf("changed target bypassed bound preview: %v", err)
	}
	assertFileHolds(t, filepath.Join(source, "main.go"), "package main\n")
	_, err = service.PreviewForSession(t.Context(), "", task.ID, "summary", nil)
	testutil.FailErr(t, "review retained checkpoint against resolved source", err)
}
