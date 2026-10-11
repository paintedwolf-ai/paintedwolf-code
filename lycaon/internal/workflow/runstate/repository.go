package runstate

import (
	"context"
	"database/sql"
	"github.com/lycaon/lycaon/internal/reviewcoverage"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/pkg/api"
)

// Repository binds the actual persistence domains used by workflow services.
type Repository struct {
	Assignments  AssignmentsRepository
	Runs         RunsRepository
	State        StateRepository
	Starts       StartsRepository
	Commands     CommandsRepository
	Blueprints   BlueprintsRepository
	Teardowns    TeardownsRepository
	Verdicts     VerdictsRepository
	Transactions TransactionsRepository
}

type RunsRepository interface {
	Get(ctx context.Context, id string) (*api.WorkflowRun, error)
	ActiveBySession(ctx context.Context, sessionID string) (*api.WorkflowRun, error)
	ActiveStateBySession(ctx context.Context, sessionID string) (*api.WorkflowRun, map[string]any, error)
	ActiveByProjectForBlueprint(ctx context.Context, projectID, blueprintPath string) (*api.WorkflowRun, error)
	ListBySession(ctx context.Context, sessionID string, limit int, statusFilter []string) ([]api.WorkflowRun, error)
	ListRunning(ctx context.Context) ([]api.WorkflowRun, error)
	ListPausedOnChild(ctx context.Context) ([]api.WorkflowRun, error)
	ListPageBySession(ctx context.Context, sessionID string, limit int, statusFilter []string, cursor string) (api.WorkflowRunPage, error)
	LatestChildByParentRunID(ctx context.Context, parentRunID string) (*api.WorkflowRun, error)
	GetScaffoldVars(ctx context.Context, workflowRunID string) (map[string]any, error)
}

type StateRepository interface {
	CreateState(ctx context.Context, run *api.WorkflowRun, projectDir string, vars map[string]any) error
	Update(ctx context.Context, run *api.WorkflowRun) error
	CommitState(ctx context.Context, run *api.WorkflowRun, projectDir string, vars map[string]any) error
	UpdateVars(ctx context.Context, run *api.WorkflowRun, projectDir string, vars map[string]any) error
}

type StartsRepository interface {
	StartChild(ctx context.Context, parent, child *api.WorkflowRun, mutation ChildStartMutation) error
	ReplayStart(ctx context.Context, operationID, sessionID, inputDigest string) (*api.WorkflowRun, bool, error)
	ActivateStart(ctx context.Context, operationID, inputDigest string, active, replacement *api.WorkflowRun, mutation StartMutation) ([]api.WorkflowRun, error)
}

type CommandsRepository interface {
	CancelActiveTree(ctx context.Context, target *api.WorkflowRun, reason string, posture api.SessionPosture, teardowns map[string]*TeardownIntent) ([]api.WorkflowRun, error)
	ReplayCommand(ctx context.Context, runID string, sourceRevision int64, kind, inputDigest string) (*api.WorkflowRun, bool, error)
	ReplayCommandOperation(ctx context.Context, operationID, kind, inputDigest string) (*api.WorkflowRun, bool, error)
	CommitCommand(ctx context.Context, run *api.WorkflowRun, mutation CommandMutation) error
}

type BlueprintsRepository interface {
	RelocateBlueprintPath(ctx context.Context, projectID, from, to string) (sessionIDs []string, err error)
	CommitBlueprintApproval(ctx context.Context, run *api.WorkflowRun, projectDir string, vars map[string]any, digest, channel string) error
	BlueprintApprovalMatches(ctx context.Context, projectID, path, runID string, revision int64, digest string) (bool, error)
}

type TeardownsRepository interface {
	PendingTeardowns(ctx context.Context) ([]TeardownIntent, error)
	CompleteTeardown(ctx context.Context, operationID string) error
	FailTeardown(ctx context.Context, operationID, detail string) error
}

type VerdictsRepository interface {
	ReadVerdictReceipts(context.Context, string) ([]VerdictReceipt, error)
	GetVerdictOperation(ctx context.Context, toolCallID string) (*VerdictOperation, bool, error)
	PrepareVerdictOperation(ctx context.Context, op VerdictOperation) (*VerdictOperation, bool, error)
	MarkVerdictEvidencePublished(ctx context.Context, toolCallID string) error
	PendingVerdictOperations(ctx context.Context) ([]VerdictOperation, error)
	CommitVerdictOperation(ctx context.Context, op VerdictOperation, run *api.WorkflowRun, projectDir string, vars map[string]any, outcome ReviewOutcome) error
	ResolveVerdictOperationDiverged(ctx context.Context, toolCallID, reason string) error
	RebaseVerdictOperation(ctx context.Context, toolCallID string, sourceRevision int64) error
}

type TransactionsRepository interface {
	MutationEventsOutboxed() bool
	SetEventOutbox(*eventoutbox.Outbox)
	SetWorkerRunnableNotifier(RunnableNotifier)
	SetSessionMutations(SessionMutations)
	SetAuthzRecorder(authzledger.TransactionalRecorder)
}

type SessionMutations interface {
	AppendMessagesTx(context.Context, *sql.Tx, string, ...api.Message) error
	SetPostureTx(context.Context, *sql.Tx, string, api.SessionPosture) error
}

type RunnableNotifier interface{ NotifyRunnable() }

type AssignmentsRepository interface {
	RecordReviewBinding(context.Context, *api.WorkflowRun, reviewcoverage.Binding) error
	ReviewBinding(context.Context, string) (*reviewcoverage.Binding, error)
	ReviewBindings(context.Context, string, string, string, int) ([]reviewcoverage.Binding, error)
	ReviewInputRevision(context.Context, string) (int64, error)
}
