package loopwake

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

type WorkflowDomains struct {
	Runs        WorkflowRuns
	Approvals   WorkflowApprovals
	Obligations WorkflowObligations
}
type WorkflowRuns interface {
	ActiveBySession(context.Context, string) (*api.WorkflowRun, error)
	GetScaffoldVars(context.Context, string) (map[string]any, error)
}
type WorkflowApprovals interface {
	HumanApprovalAwaiting(context.Context, string) (bool, error)
}
type WorkflowObligations interface {
	HostObligationHeld(context.Context, string) (bool, error)
	HostObligationHoldKinds(context.Context, string) []string
}
type LoopDeps struct {
	HostTurnBlocked func(context.Context, string) bool

	RunPrompt                 func(ctx context.Context, sessionID string) (*promptresult.Result, error)
	RunWaitResume             func(ctx context.Context, sessionID string, delivery WaitDelivery) (*promptresult.Result, error)
	GetSession                func(ctx context.Context, sessionID string) (*api.Session, error)
	Limits                    func(context.Context, *api.Session) settings.SessionLimits
	IsEscalated               func(sessionID string) bool
	WorkflowSource            *WorkflowDomains
	CoordinatorFrame          inject.CoordinatorTurnFrameSource
	BoardWillForceInject      func(ctx context.Context, sess *api.Session, run api.CoordinatorRunContext) bool
	QueueInform               func(ctx context.Context, sessionID string, inform anchor.ID, env anchor.Envelope)
	HasQueuedKick             func(sessionID, kickID string) bool
	IsCoordinatorSession      func(ctx context.Context, sess *api.Session) bool
	WorkerCycleIdle           WorkerCycleIdle
	HostWakeActionable        func(ctx context.Context, in HostWakeActionableInput) bool
	WorkflowObligationsOpen   func(ctx context.Context, sessionID string) bool
	HostWakeOverlayPromoteDue func(ctx context.Context, sessionID string) bool
	ScanCycleOpen             func(ctx context.Context, sessionID string) bool
	ProcessRunning            func(sessionID string, handles []string) bool
	ProcessState              func(sessionID, handle string) (known, running bool)
	// ProcessReport returns the published account of an ended process job;
	// false until its completion is published.
	ProcessReport                  func(sessionID, handle string) (report string, published bool)
	DropPendingKicksForBatchSeq    func(sessionID string, batchSeq int)
	DropPendingKicksBeforeBatchSeq func(sessionID string, liveSeq int)
	OnLoopQuiescent                func(ctx context.Context, sessionID string)
	// PublishWaitLease brackets an armed host-mover sleep as a session activity lease.
	PublishWaitLease func(ctx context.Context, sessionID string, lease WaitLease)
}
