package worker_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
)

func TestExecuteFailureCodeLLMTimeout(t *testing.T) {
	err := errors.Join(promptloop.ErrLLMTurnTimeout, context.DeadlineExceeded)
	if got := worker.ExecuteFailureCode(err); got != "worker_closeout_exhausted" {
		t.Fatalf("ExecuteFailureCode = %q want worker_closeout_exhausted", got)
	}
}

func TestExecuteFailureCodeWorkspaceSetup(t *testing.T) {
	if got := worker.ExecuteFailureCode(errors.Join(worker.ErrWorkerBranchClaimFailed, workspace.ErrWorkspaceStorageExhausted)); got != "worker_workspace_disk_full" {
		t.Fatalf("storage failure code = %q want worker_workspace_disk_full", got)
	}
	if got := worker.ExecuteFailureCode(errors.Join(worker.ErrWorkerBranchClaimFailed, errors.New("permission denied"))); got != "worker_workspace_unavailable" {
		t.Fatalf("workspace failure code = %q want worker_workspace_unavailable", got)
	}
	if got := worker.ExecuteFailureCode(promptloop.ErrOwnerUnsettled); got != "worker_owner_unsettled" {
		t.Fatalf("owner unsettled code = %q want worker_owner_unsettled", got)
	}
}
