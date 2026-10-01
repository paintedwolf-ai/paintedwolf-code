package worker

import (
	"errors"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// ExecuteFailureRenderer renders Den-facing worker execute failure copy.
type ExecuteFailureRenderer interface {
	RenderExecuteFailure(err error) api.WorkerFailure
}

// ExecuteFailureCode maps a worker execute error to a user-notice code.
func ExecuteFailureCode(err error) string {
	if err == nil {
		return "worker_execute_failed"
	}
	if errors.Is(err, promptloop.ErrLLMTurnTimeout) {
		return "worker_closeout_exhausted"
	}
	if errors.Is(err, workspace.ErrWorkspaceStorageExhausted) {
		return "worker_workspace_disk_full"
	}
	if errors.Is(err, ErrWorkerBranchClaimFailed) {
		return "worker_workspace_unavailable"
	}
	if errors.Is(err, promptloop.ErrOwnerUnsettled) {
		return "worker_owner_unsettled"
	}
	return "worker_execute_failed"
}
