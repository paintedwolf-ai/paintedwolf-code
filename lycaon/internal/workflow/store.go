package workflow

import (
	"context"

	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/pkg/api"
)

// RunStore persists workflow run instances.
type RunStore interface {
	ReviewInputRevision(context.Context, string) (int64, error)
	RecordReviewBinding(context.Context, *api.WorkflowRun, reviewcoverage.Binding) error
	ReviewBinding(context.Context, string) (*reviewcoverage.Binding, error)
	ReviewBindings(context.Context, string, string, string, int) ([]reviewcoverage.Binding, error)

	MutationEventsOutboxed() bool
	CreateState(ctx context.Context, run *api.WorkflowRun, projectDir string, vars map[string]any) error
	StartChild(ctx context.Context, parent, child *api.WorkflowRun, mutation workflowChildStartMutation) error
	CancelActiveTree(ctx context.Context, target *api.WorkflowRun, reason string, posture api.SessionPosture, teardowns map[string]*workflowTeardownIntent) ([]api.WorkflowRun, error)
	Get(ctx context.Context, id string) (*api.WorkflowRun, error)
	Update(ctx context.Context, run *api.WorkflowRun) error
	CommitState(ctx context.Context, run *api.WorkflowRun, projectDir string, vars map[string]any) error
	UpdateVars(ctx context.Context, run *api.WorkflowRun, projectDir string, vars map[string]any) error
	ActiveBySession(ctx context.Context, sessionID string) (*api.WorkflowRun, error)
	ActiveStateBySession(ctx context.Context, sessionID string) (*api.WorkflowRun, map[string]any, error)
	ActiveByProjectForBlueprint(ctx context.Context, projectID, blueprintPath string) (*api.WorkflowRun, error)
	RelocateBlueprintPath(ctx context.Context, projectID, from, to string) (sessionIDs []string, err error)
	ListBySession(ctx context.Context, sessionID string, limit int, statusFilter []string) ([]api.WorkflowRun, error)
	ListRunning(ctx context.Context) ([]api.WorkflowRun, error)
	ListPausedOnChild(ctx context.Context) ([]api.WorkflowRun, error)
	ListPageBySession(ctx context.Context, sessionID string, limit int, statusFilter []string, cursor string) (api.WorkflowRunPage, error)
	LatestChildByParentRunID(ctx context.Context, parentRunID string) (*api.WorkflowRun, error)
	GetScaffoldVars(ctx context.Context, workflowRunID string) (map[string]any, error)
	ReplayCommand(ctx context.Context, runID string, sourceRevision int64, kind, inputDigest string) (*api.WorkflowRun, bool, error)
	ReplayCommandOperation(ctx context.Context, operationID, kind, inputDigest string) (*api.WorkflowRun, bool, error)
	CommitCommand(ctx context.Context, run *api.WorkflowRun, mutation workflowCommandMutation) error
	ReplayStart(ctx context.Context, operationID, sessionID, inputDigest string) (*api.WorkflowRun, bool, error)
	ActivateStart(ctx context.Context, operationID, inputDigest string, active, replacement *api.WorkflowRun, mutation workflowStartMutation) ([]api.WorkflowRun, error)
	PendingTeardowns(ctx context.Context) ([]workflowTeardownIntent, error)
	CompleteTeardown(ctx context.Context, operationID string) error
	FailTeardown(ctx context.Context, operationID, detail string) error
	CommitBlueprintApproval(ctx context.Context, run *api.WorkflowRun, projectDir string, vars map[string]any, digest, channel string) error
	BlueprintApprovalMatches(ctx context.Context, projectID, path, runID string, revision int64, digest string) (bool, error)
	getVerdictOperation(ctx context.Context, toolCallID string) (*verdictOperation, bool, error)
	prepareVerdictOperation(ctx context.Context, op verdictOperation) (*verdictOperation, bool, error)
	markVerdictEvidencePublished(ctx context.Context, toolCallID string) error
	pendingVerdictOperations(ctx context.Context) ([]verdictOperation, error)
	commitVerdictOperation(ctx context.Context, op verdictOperation, run *api.WorkflowRun, projectDir string, vars map[string]any, outcome ReviewLoopVerdictOutcome) error
	resolveVerdictOperationDiverged(ctx context.Context, toolCallID, reason string) error
	rebaseVerdictOperation(ctx context.Context, toolCallID string, sourceRevision int64) error
}
