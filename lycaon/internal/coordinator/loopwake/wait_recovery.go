package loopwake

import (
	"context"
	"strings"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/tools"
)

func restoreWaitRequest(ctx context.Context, loop *LoopEngine, store *awaitstore.Store, tctx tools.ToolContext, request *waitRequest) error {
	if strings.TrimSpace(tctx.WorkerJobID) == "" {
		if subscription, complete := loop.runtimeWaitSubscription(tctx.SessionID); complete {
			if !request.ExplicitConditions {
				request.Conditions = subscription.Conditions
				request.Triggers = subscription.Triggers
				request.ProcessHandles = subscription.ProcessHandles
				request.WorkerHandles = subscription.WorkerHandles
			}
			if !request.ExplicitMode {
				request.UntilComplete = subscription.UntilComplete
				request.ResumeBounded = subscription.Bounded
			}
			return nil
		}
	}
	lease, found, err := store.LatestResumeCandidate(ctx, tctx.SessionID)
	if err != nil || !found {
		return err
	}
	if !request.ExplicitMode {
		request.ResumeDeadline = lease.Deadline
		request.ResumeBounded = !lease.Deadline.IsZero()
		request.UntilComplete = lease.UntilComplete
	}
	if !request.ExplicitConditions {
		request.Conditions = append([]awaitstore.Condition(nil), lease.Conditions...)
		request.Triggers, request.ProcessHandles = triggersFromConditions(request.Conditions)
		request.WorkerHandles = workerHandlesFromConditions(request.Conditions)
	}
	return nil
}

func (l *LoopEngine) runtimeWaitSubscription(sessionID string) (waitSubscription, bool) {
	value, found := l.sleep.Load(sessionID)
	if !found {
		return waitSubscription{}, false
	}
	state := value.(*sessionSleep)
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.waitTriggers) == 0 {
		return waitSubscription{}, false
	}
	for _, trigger := range state.waitTriggers {
		// Readiness parameters are retained only in the durable lease.
		if trigger == WaitTriggerHTTPReady || trigger == WaitTriggerPortReady {
			return waitSubscription{}, false
		}
	}
	_, bounded := waitTriggerSet(state.waitTriggers)[WaitTriggerTimer]
	subscription := waitSubscription{
		UntilComplete:  state.untilComplete,
		Bounded:        bounded,
		Triggers:       append([]WaitTrigger(nil), state.waitTriggers...),
		ProcessHandles: append([]string(nil), state.processHandles...),
		WorkerHandles:  append([]string(nil), state.workerHandles...),
	}
	subscription.Conditions = conditionsFromTriggers(subscription.Triggers, subscription.ProcessHandles, subscription.WorkerHandles)
	return subscription, true
}

// RecoverWaitLeases reconstructs timers, subscriptions, and active probes after boot.
// Worker deadlines and runnable transitions remain owned by the worker queue transaction.
func RecoverWaitLeases(ctx context.Context, loop *LoopEngine, store *awaitstore.Store) error {
	if loop == nil || store == nil {
		return nil
	}
	leases, err := store.Active(ctx)
	if err != nil {
		return err
	}
	for _, lease := range leases {
		triggers := []WaitTrigger{WaitTriggerTimer}
		var handles []string
		for _, condition := range lease.Conditions {
			triggers = append(triggers, WaitTrigger(condition.Kind))
			if condition.Kind == string(WaitTriggerProcessDone) {
				handles = append(handles, condition.Handles...)
			}
		}
		if lease.Deadline.IsZero() || strings.TrimSpace(lease.WorkerJobID) != "" {
			triggers = removeWaitTrigger(triggers, WaitTriggerTimer)
		}
		loop.enterSleep(ctx, lease.SessionID, sleepArm{
			until: lease.Deadline, untilComplete: lease.UntilComplete, reason: lease.Reason,
			triggers: triggers, processHandles: handles, workerHandles: workerHandlesFromConditions(lease.Conditions), mover: SleepMoverHost,
		})
		startConditionMonitor(ctx, loop, store, lease)
	}
	pending, err := store.PendingAgentResumes(ctx)
	if err != nil {
		return err
	}
	for _, lease := range pending {
		loop.rememberWaitWinner(lease.SessionID, lease.ID, lease.Winner)
		loop.Nudge(ctx, lease.SessionID, anchor.LoopWake, anchor.LoopWake, lease.ID, anchor.Envelope{})
	}
	return nil
}
