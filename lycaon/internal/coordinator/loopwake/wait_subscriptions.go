package loopwake

import (
	"context"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"sync"
)

type WaitSubscriptionsDeps struct {
	GetSession                func(ctx context.Context, sessionID string) (*api.Session, error)
	HostWakeOverlayPromoteDue func(ctx context.Context, sessionID string) bool
	ProcessReport             func(sessionID, handle string) (report string, published bool)
	ProcessRunning            func(sessionID string, handles []string) bool
	ProcessState              func(sessionID, handle string) (known, running bool)
	ScanCycleOpen             func(ctx context.Context, sessionID string) bool
	WorkerCycleIdle           WorkerCycleIdle
}
type WaitSubscriptions struct {
	Policy *HostWakePolicy

	depsMu     sync.RWMutex
	deps       WaitSubscriptionsDeps
	waitStore  *awaitstore.Store
	Nudges     *Nudges
	Facts      *SessionFacts
	Deliveries *WaitDeliveries
	Waits      *Waits
	Cycles     *WorkerCycles
}

func (l *WaitSubscriptions) setDeps(deps WaitSubscriptionsDeps) {
	l.depsMu.Lock()
	l.deps = deps
	l.depsMu.Unlock()
}
func (l *WaitSubscriptions) loopDeps() WaitSubscriptionsDeps {
	if l == nil {
		return WaitSubscriptionsDeps{}
	}
	l.depsMu.RLock()
	defer l.depsMu.RUnlock()
	return l.deps
}
func (l *WaitSubscriptions) SetWaitStore(store *awaitstore.Store) {
	if l == nil {
		return
	}
	l.depsMu.Lock()
	l.waitStore = store
	l.depsMu.Unlock()
}
func (l *WaitSubscriptions) durableWaitStore() *awaitstore.Store {
	l.depsMu.RLock()
	defer l.depsMu.RUnlock()
	return l.waitStore
}
func (l *WaitSubscriptions) settleDurableWaitWake(
	ctx context.Context,
	sessionID string,
	wake, inform anchor.ID,
	legID, completingJobID string,
	env anchor.Envelope,
) bool {
	store := l.durableWaitStore()
	if store == nil {
		return false
	}
	lease, active, err := store.ForSession(ctx, sessionID)
	if err != nil {
		return true
	}
	if !active {
		return false
	}
	// The durable subscription exists before its sleep projection is armed.
	triggers, processHandles := triggersFromConditions(lease.Conditions)
	condition, matched := waitConditionForWake(triggers, waitMatchInput{
		Wake: wake, CompletingJobID: completingJobID, ProcessHandle: legID,
		ProcessHandles:    processHandles,
		WorkerHandles:     workerHandlesFromConditions(lease.Conditions),
		CycleIdle:         l.Cycles.workerCycleIdle(ctx, sessionID, completingJobID),
		OverlayPromoteDue: l.overlayPromoteDue(ctx, sessionID, env),
		NeedsDecision:     env.HasWorkerDecision(),
	})
	condition.Report = processWakeReport(wake, env)
	if condition.Outcome == "" {
		condition.Outcome = "satisfied"
	}
	if strings.TrimSpace(lease.WorkerJobID) != "" {
		// Only a subscribed event settles a worker-owned wait.
		if !matched {
			loopLogNudge(sessionID, wake, inform, legID, completingJobID, "worker_wait_subscription_filtered")
			return true
		}
		won, settleErr := store.SettleLease(ctx, lease.ID, "resolved", condition)
		if settleErr != nil || !won {
			return true
		}
		l.Waits.breakSleep(ctx, sessionID, string(wake), true)
		return true
	}
	var won bool
	if matched {
		won, err = store.SettleLease(ctx, lease.ID, "resolved", condition)
	} else {
		won, err = store.SettleLease(ctx, lease.ID, "interrupted", awaitstore.Condition{})
	}
	if err != nil || !won {
		return true
	}
	if matched {
		l.Deliveries.rememberWaitWinner(sessionID, lease.ID, condition)
		l.Waits.breakSleep(ctx, sessionID, string(wake), false)
		l.Deliveries.runWaitResumeAsync(ctx, sessionID)
		return true
	}
	return false
}
