package workernotice_test

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/usernotice"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workernotice"
	"github.com/lycaon/lycaon/internal/workspace"
)

func loadWorkerFailureNoticeCatalog(t *testing.T) *usernotice.Catalog {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	cfg, err := usernotice.LoadNoticeDir(filepath.Join(root, "config", "packs", "painted-wolf", "platform", "host", "user-notices"))
	testutil.FailErr(t, "usernotice.Load", err)
	return usernotice.NewCatalog(cfg)
}

func TestRenderExecuteFailureCloseoutExhausted(t *testing.T) {
	renderer := workernotice.NewRenderer(loadWorkerFailureNoticeCatalog(t))
	err := errors.Join(promptloop.ErrLLMTurnTimeout, context.DeadlineExceeded)
	out := renderer.RenderExecuteFailure(err)
	if out.Code != "worker_closeout_exhausted" {
		t.Fatalf("Code = %q want worker_closeout_exhausted", out.Code)
	}
	if strings.TrimSpace(out.Title) == "" || strings.TrimSpace(out.Message) == "" {
		t.Fatalf("RenderExecuteFailure = %+v want title and message", out)
	}
	if !strings.Contains(out.Message, "closing attempts") {
		t.Fatalf("Message = %q want closing attempts copy", out.Message)
	}
}

func TestRenderExecuteFailureOwnerUnsettled(t *testing.T) {
	renderer := workernotice.NewRenderer(loadWorkerFailureNoticeCatalog(t))
	out := renderer.RenderExecuteFailure(promptloop.ErrOwnerUnsettled)
	if out.Code != "worker_owner_unsettled" {
		t.Fatalf("Code = %q want worker_owner_unsettled", out.Code)
	}
	if !strings.Contains(out.Message, "not a workspace-setup") {
		t.Fatalf("Message = %q want subsystem-owner failure copy", out.Message)
	}
}

func TestRenderExecuteFailureWorkspaceUnavailable(t *testing.T) {
	renderer := workernotice.NewRenderer(loadWorkerFailureNoticeCatalog(t))
	out := renderer.RenderExecuteFailure(errors.Join(worker.ErrWorkerBranchClaimFailed, errors.New("setup failed")))
	if out.Code != "worker_workspace_unavailable" {
		t.Fatalf("Code = %q want worker_workspace_unavailable", out.Code)
	}
	if !strings.Contains(out.Message, "No worker changes") {
		t.Fatalf("Message = %q want no-changes explanation", out.Message)
	}
}

func TestRenderExecuteFailureWorkspaceStorageFull(t *testing.T) {
	renderer := workernotice.NewRenderer(loadWorkerFailureNoticeCatalog(t))
	out := renderer.RenderExecuteFailure(errors.Join(worker.ErrWorkerBranchClaimFailed, workspace.ErrWorkspaceStorageExhausted))
	if out.Code != "worker_workspace_disk_full" {
		t.Fatalf("Code = %q want worker_workspace_disk_full", out.Code)
	}
	if !strings.Contains(out.SuggestedAction, "clear reusable workspace caches") {
		t.Fatalf("SuggestedAction = %q want cache-clear repair", out.SuggestedAction)
	}
}

func TestRenderExecuteFailureWorkspaceCapacityDetails(t *testing.T) {
	renderer := workernotice.NewRenderer(loadWorkerFailureNoticeCatalog(t))
	err := errors.Join(
		worker.ErrWorkerBranchClaimFailed,
		workspace.ErrWorkspaceStorageExhausted,
		&workspace.CapacityError{Path: "/data", Required: 30 << 30, Available: 12 << 30},
	)
	out := renderer.RenderExecuteFailure(err)
	if !strings.Contains(out.Message, "30.0 GiB") || !strings.Contains(out.Message, "12.0 GiB") {
		t.Fatalf("Message = %q want required and available capacity", out.Message)
	}
}
