package lifecycle

import (
	"context"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"time"
)

type RunReader interface {
	Get(context.Context, string) (*api.WorkflowRun, error)
	ActiveBySession(context.Context, string) (*api.WorkflowRun, error)
	GetScaffoldVars(context.Context, string) (map[string]any, error)
	ListBySession(context.Context, string, int, []string) ([]api.WorkflowRun, error)
}

type StartRepository interface {
	ReplayStart(context.Context, string, string, string) (*api.WorkflowRun, bool, error)
	ActivateStart(context.Context, string, string, *api.WorkflowRun, *api.WorkflowRun, runstate.StartMutation) ([]api.WorkflowRun, error)
}

type TreeCancellation interface {
	CancelActiveTree(context.Context, *api.WorkflowRun, string, api.SessionPosture, map[string]*runstate.TeardownIntent) ([]api.WorkflowRun, error)
}

type TeardownRepository interface {
	PendingTeardowns(context.Context) ([]runstate.TeardownIntent, error)
	CompleteTeardown(context.Context, string) error
	FailTeardown(context.Context, string, string) error
}

type Sessions interface {
	Get(context.Context, string) (*api.Session, error)
	LatestTurnProgress(context.Context, string) (time.Time, error)
}

type WorkerStop interface {
	CancelWorkersByRunID(context.Context, string, string) error
	SettleWorkerCancellationsByRunID(context.Context, string, string) error
	HoldPendingWorkersByRunID(context.Context, string) error
	CancelDelegationsByRunID(context.Context, string) error
}

type StartBarrier interface {
	WithSessionTreeAdmission(context.Context, string, func() error) error
}
type ExitBarrier interface {
	WithSessionTreeStop(context.Context, string, string, func(context.Context) error) error
}

type Publication interface {
	Publish(context.Context, *api.Session, *api.WorkflowRun)
	PublishSession(context.Context, *api.WorkflowRun)
}

type Plans interface {
	ResolvePlanForStart(context.Context, api.StartWorkflowRunRequest, string, string, string, string, string) (string, error)
	SyncTranscriptForRun(context.Context, *api.WorkflowRun, bool) error
}
type Approvals interface {
	AutoApproveOnPhase(context.Context, *api.WorkflowRun, workflowdef.Manifest, workflowdef.PhaseDef, map[string]any) (map[string]any, error)
	AwaitsHumanApproval(context.Context, *api.WorkflowRun, map[string]any) (bool, error)
}

type Phases interface {
	InitializeVars(context.Context, *api.Session, *api.WorkflowRun, workflowdef.Manifest, map[string]any) (map[string]any, error)
	ActivateInitial(context.Context, *api.WorkflowRun, workflowdef.Manifest, string, bool) (*api.WorkflowRun, error)
	TryAutoAdvance(context.Context, string) (*api.WorkflowRun, error)
}

type RequestAdmission interface {
	NotifyAccepted(context.Context, string, string)
}
type RequestRecovery interface {
	ResumeResolvedRequestPhase(context.Context, *api.WorkflowRun) (bool, error)
}
type FeedbackNotifications interface {
	NotifyPending(context.Context, string, map[string]any)
}
type StartScaffold interface {
	ClearStartState(context.Context, string)
}
type AskCancellation interface {
	CancelPendingAsk(map[string]any, string) map[string]any
}

type Settlement interface {
	ResumeParentAfterChildExit(context.Context, *api.WorkflowRun, string) (*api.WorkflowRun, error)
	ReconcileTerminalRun(context.Context, *api.WorkflowRun) error
}

type Ambient interface {
	EnsureSessionWorkflow(context.Context, string) (*api.WorkflowRun, error)
}

type Reports interface {
	RecoverReportDelivery(context.Context, *api.WorkflowRun) (bool, error)
}
