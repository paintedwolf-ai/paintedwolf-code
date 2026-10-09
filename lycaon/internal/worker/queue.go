// Package worker defines the persistent worker queue and in-session worker ring.
package worker

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/workerprogress"
	"github.com/lycaon/lycaon/pkg/api"
)

// TaskAdmission guards worker admission against session stop.
type TaskAdmission interface {
	WithTaskAdmission(ctx context.Context, task api.WorkerTask, fn func() error) error
}

// ClaimRequest selects which pending jobs a claimer may take.
type ClaimRequest struct {
	ProjectID       string
	ClaimedBy       string
	ExecutionTarget api.ExecutionTarget
}

// WorkerQueue persists worker jobs from pending through terminal state.
type WorkerQueue interface {
	TaskAdmission
	Enqueue(ctx context.Context, task api.WorkerTask) (string, error)
	PrepareEnqueue(ctx context.Context, projectID string, task *api.WorkerTask) error
	PublishEnqueued(ctx context.Context, jobID string)
	ClaimNext(ctx context.Context, req ClaimRequest) (*api.WorkerTask, error)
	Complete(ctx context.Context, claimed *api.WorkerTask, result api.WorkerResult) (bool, error)
	// Retry closes the current attempt and returns the semantic job to pending.
	Retry(ctx context.Context, claimed *api.WorkerTask, err error) (bool, error)
	Park(ctx context.Context, claimed *api.WorkerTask) (bool, error)
	ResumeReadyWaits(ctx context.Context) (int, error)
	Fail(ctx context.Context, claimed *api.WorkerTask, err error) (bool, error)
	RequestCancellation(ctx context.Context, id string) (bool, error)
	RequestClaimCancellation(ctx context.Context, claimed *api.WorkerTask) (bool, error)
	// StopExecution fences the job and joins runtime without discarding its workspace.
	StopExecution(ctx context.Context, id string) error
	Cancel(ctx context.Context, id string, result *api.WorkerResult) error
	FinishCanceled(ctx context.Context, id string, result *api.WorkerResult) error
	Hold(ctx context.Context, id string) error
	List(ctx context.Context, projectID string, status ...api.WorkerStatus) ([]api.WorkerTask, error)
	ListSessionPage(ctx context.Context, query SessionPageQuery) (SessionPage, error)
	ListBySession(ctx context.Context, projectID, sessionID string, status ...api.WorkerStatus) ([]api.WorkerTask, error)
	ListByWorkflowRunID(ctx context.Context, runID string, status ...api.WorkerStatus) ([]api.WorkerTask, error)
	ListCancellationRequestsByRunID(ctx context.Context, runID string) ([]api.WorkerTask, error)
	ListByWorkspacePath(ctx context.Context, workspacePath string, status ...api.WorkerStatus) ([]api.WorkerTask, error)
	Get(jobID string) (*api.WorkerTask, bool)
	GetLatestByChildSessionID(ctx context.Context, childSessionID string) (*api.WorkerTask, bool)
	SetChildSessionID(ctx context.Context, jobID, childSessionID string) error
	PublishWorkerProgress(ctx context.Context, workerJobID string, snap workerprogress.Snapshot, checkpoint bool) error
	RenewClaim(ctx context.Context, jobID, claimToken string) (bool, error)
	RecoverExpiredClaims(ctx context.Context) ([]api.WorkerTask, error)
	ListPendingOutcomes(ctx context.Context) ([]api.WorkerTask, error)
	MarkOutcomeDelivered(ctx context.Context, jobID string) error
	// ClaimWorkerBranch provisions a write worker's branch on first use.
	ClaimWorkerBranch(ctx context.Context, jobID string) (*api.WorkerTask, error)
	// EnsureWorkerBranch materializes a claimed branch and leases it to the caller.
	EnsureWorkerBranch(ctx context.Context, jobID string) (*api.WorkerTask, *BranchLease, error)
	// ListBranchJobs includes jobs whose branch is still being provisioned.
	ListBranchJobs(ctx context.Context) ([]BranchJob, error)
}

// admitWorkerTransition holds session admission until the claim transition commits.
func admitWorkerTransition(ctx context.Context, admission TaskAdmission, claimed *api.WorkerTask, transition func() (bool, error)) (bool, error) {
	if claimed == nil {
		return false, fmt.Errorf("claimed worker task required")
	}
	var won bool
	err := admission.WithTaskAdmission(ctx, *claimed, func() error {
		var err error
		won, err = transition()
		return err
	})
	return won, err
}

// WorkflowDomains binds independent run and task admission resources.
type WorkflowDomains struct {
	Runs  WorkflowRunAdmission
	Tasks WorkflowTaskAdmission
}

type WorkflowRunAdmission interface {
	AssertRunnable(ctx context.Context, runID string) error
}
type WorkflowTaskAdmission interface {
	AssertWorkerTask(ctx context.Context, task *api.WorkerTask) error
}
