package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"sync"
	"time"
)

const (
	defaultCoordinatorWaitInterval = 2 * time.Minute
	// defaultWorkflowObligationInterval paces unmet workflow obligations.
	defaultWorkflowObligationInterval = 2 * time.Minute
)

// WorkflowDomains binds the run state and wait policies used by coordinator loops.
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

// LoopDeps wires coordinator loop policy and prompt execution.
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

type loopKickKey struct {
	sessionID string
	runID     string
	legID     string
	wake      anchor.ID
}

type loopKickStamp struct {
	at time.Time
}

type loopBudgetKey struct {
	sessionID string
	runID     string
}

// asyncTurnWork tracks one cancelable host turn.
type asyncTurnWork struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type sessionSleep struct {
	mu               sync.Mutex
	armed            bool
	untilComplete    bool
	until            time.Time
	interruptedUntil time.Time
	reason           string
	timer            *time.Timer
	timerDone        chan struct{}
	timerGeneration  uint64
	waitThisTurn     bool
	waitTriggers     []WaitTrigger
	processHandles   []string
	workerHandles    []string
	// mover identifies who may end the sleep.
	mover SleepMover
	// activityID is non-empty exactly while an awaiting_wake lease is open.
	activityID        string
	activityStartedAt time.Time
}


// UserTurnContinuation describes the next visible-turn transition.
type UserTurnContinuation uint8

const (
	// UserTurnSettled means no host continuation remains.
	UserTurnSettled UserTurnContinuation = iota
	// UserTurnContinues keeps the visible turn open.
	UserTurnContinues
)

// Nonzero size gives each live execution owner a distinct pointer.
type promptExecutionToken struct{ _ byte }

// processWakeReport is the host's account a process wake carries to the
// resumed turn.
func processWakeReport(wake anchor.ID, env anchor.Envelope) string {
	switch wake {
	case anchor.ProcessFinished:
		return strings.TrimSpace(env.CommandCompletionDigest)
	case anchor.ProcessRefused:
		return strings.TrimSpace(env.CommandRefusalDigest)
	default:
		return ""
	}
}

func alwaysBreaksSleep(wake anchor.ID, completingJobID string) bool {
	if strings.TrimSpace(completingJobID) != "" {
		return true
	}
	switch wake {
	case anchor.LegFinished, anchor.WorkerTaskFinished, anchor.WorkerBudgetRequested:
		return true
	default:
		return false
	}
}

// sleepArm is one complete sleep subscription, applied under the sleep lock.
type sleepArm struct {
	until          time.Time
	untilComplete  bool
	reason         string
	triggers       []WaitTrigger
	processHandles []string
	workerHandles  []string
	mover          SleepMover
}

func cancelSleepTimerLocked(st *sessionSleep) {
	st.timerGeneration++
	if st.timer != nil {
		if st.timer.Stop() && st.timerDone != nil {
			close(st.timerDone)
		}
		st.timer = nil
		st.timerDone = nil
	}
}

func sleepArmedLocked(st *sessionSleep, now time.Time) bool {
	if st == nil || !st.armed {
		return false
	}
	if _, hasTimer := waitTriggerSet(st.waitTriggers)[WaitTriggerTimer]; !hasTimer {
		return true
	}
	return now.Before(st.until)
}

// Read failures do not infer an approval park.

// Read failures do not infer a host-obligation hold.

func kickDedupLegID(inform anchor.ID, legID, completingJobID string) string {
	if inform == anchor.WorkerTaskFinished {
		if jobID := strings.TrimSpace(completingJobID); jobID != "" {
			return jobID
		}
	}
	return legID
}

// CapWaitDuration clamps a wait() timer to [MinWaitSeconds, maxSleep].
func CapWaitDuration(requested, maxSleep time.Duration) time.Duration {
	if requested <= 0 {
		requested = DefaultWaitSeconds * time.Second
	}
	minimum := MinWaitSeconds * time.Second
	if requested < minimum {
		requested = minimum
	}
	d := requested
	if maxSleep > 0 && d > maxSleep {
		return maxSleep
	}
	return d
}
