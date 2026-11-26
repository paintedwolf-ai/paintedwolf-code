package loopwake

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
)

// ActivityKind names host work that can still move a session forward.
type ActivityKind string

const (
	ActivityPromptSubmission ActivityKind = "prompt_submission"
	ActivityPromptExecution  ActivityKind = "prompt_execution"
	ActivityHostTurn         ActivityKind = "host_turn"
	ActivityTurnSettlement   ActivityKind = "turn_settlement"
	ActivityWakeDelivery     ActivityKind = "wake_delivery"
	ActivityWakeDrain        ActivityKind = "wake_drain"
	ActivityQueuedWake       ActivityKind = "queued_wake"
	ActivityDeferredWake     ActivityKind = "deferred_wake"
	ActivityWaitResume       ActivityKind = "wait_resume"
	ActivitySleep            ActivityKind = "sleep"
	ActivityWaitTimer        ActivityKind = "wait_timer"
	ActivityWorkerCycle      ActivityKind = "worker_cycle"
	ActivityRoundDrain       ActivityKind = "round_drain"
	// ActivityInTransition reports work that changed while it was being read.
	ActivityInTransition ActivityKind = "in_transition"
)

// wakeLedgerCapacity bounds the decisions kept per session.
const wakeLedgerCapacity = 32

// WakeDecision records one host wake and what the loop did with it.
type WakeDecision struct {
	Wake    anchor.ID
	Inform  anchor.ID
	LegID   string
	JobID   string
	Outcome string
}

// SessionActivity is a session's host work at one instant. Busy is empty when
// nothing in the host can still move the session; Epoch changes whenever host
// work begins, ends or decides a wake.
type SessionActivity struct {
	Epoch     uint64
	Busy      []ActivityKind
	Decisions []WakeDecision
}

// HostActivity counts in-flight host work and keeps each session's recent wake
// decisions.
type HostActivity struct {
	mu        sync.Mutex
	epoch     uint64
	work      map[string]map[ActivityKind]int
	decisions map[string][]WakeDecision
}

// Begin marks work for the session until the returned function runs.
func (a *HostActivity) Begin(sessionID string, kind ActivityKind) func() {
	sessionID = strings.TrimSpace(sessionID)
	if a == nil || sessionID == "" {
		return func() {}
	}
	a.mu.Lock()
	if a.work == nil {
		a.work = make(map[string]map[ActivityKind]int)
	}
	if a.work[sessionID] == nil {
		a.work[sessionID] = make(map[ActivityKind]int)
	}
	a.work[sessionID][kind]++
	a.epoch++
	a.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.epoch++
			kinds := a.work[sessionID]
			if kinds[kind]--; kinds[kind] <= 0 {
				delete(kinds, kind)
			}
			if len(kinds) == 0 {
				delete(a.work, sessionID)
			}
		})
	}
}

// BeginPromptSubmission marks a prompt from its submission until it returns,
// covering the admission that precedes its execution.
func (l *Nudges) BeginPromptSubmission(sessionID string) func() {
	return l.Activity.Begin(sessionID, ActivityPromptSubmission)
}

// BeginRoundDrain marks a round-end pass dispatching queued prompts.
func (l *Nudges) BeginRoundDrain(sessionID string) func() {
	return l.Activity.Begin(sessionID, ActivityRoundDrain)
}

// decide records a wake outcome and logs it.
func (a *HostActivity) decide(sessionID string, wake, inform anchor.ID, legID, jobID, outcome string) {
	loopLogNudge(sessionID, wake, inform, legID, jobID, outcome)
	if a == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.epoch++
	if a.decisions == nil {
		a.decisions = make(map[string][]WakeDecision)
	}
	ledger := append(a.decisions[sessionID], WakeDecision{Wake: wake, Inform: inform, LegID: legID, JobID: jobID, Outcome: outcome})
	if len(ledger) > wakeLedgerCapacity {
		ledger = slices.Clone(ledger[len(ledger)-wakeLedgerCapacity:])
	}
	a.decisions[sessionID] = ledger
}

func (a *HostActivity) snapshot(sessionID string) (uint64, []ActivityKind, []WakeDecision) {
	if a == nil {
		return 0, nil, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	kinds := make([]ActivityKind, 0, len(a.work[sessionID]))
	for kind := range a.work[sessionID] {
		kinds = append(kinds, kind)
	}
	return a.epoch, kinds, slices.Clone(a.decisions[sessionID])
}

func (a *HostActivity) forget(sessionID string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.decisions, sessionID)
}

// Activity reports the session's host work. Loop state that holds a pending
// wake or a parked wait counts as work, so an idle session has nothing that
// could still deliver one.
func (l *LoopEngine) Activity(ctx context.Context, sessionID string) SessionActivity {
	if l == nil {
		return SessionActivity{}
	}
	sessionID = strings.TrimSpace(sessionID)
	activity := l.Nudges.Activity
	epoch, busy, decisions := activity.snapshot(sessionID)
	add := func(kind ActivityKind, on bool) {
		if on && !slices.Contains(busy, kind) {
			busy = append(busy, kind)
		}
	}
	add(ActivityPromptExecution, l.Admission.PromptExecutionActive(sessionID))
	add(ActivityTurnSettlement, l.Turns.Active(sessionID))
	_, draining := l.Nudges.pendingDrain.Load(sessionID)
	add(ActivityWakeDrain, draining)
	_, queued := l.Nudges.Pending(sessionID)
	add(ActivityQueuedWake, queued)
	add(ActivityDeferredWake, l.Nudges.sessionDeferredQueue(sessionID).nonEmpty())
	_, resume := l.Deliveries.waitWinner(sessionID)
	add(ActivityWaitResume, resume)
	add(ActivitySleep, l.Waits.sleepPending(sessionID))
	add(ActivityWorkerCycle, !l.Cycles.workerCycleIdle(ctx, sessionID, ""))
	if after, _, _ := activity.snapshot(sessionID); after != epoch {
		// Work changed while this was read; report it as busy rather than idle.
		add(ActivityInTransition, true)
	}
	slices.Sort(busy)
	return SessionActivity{Epoch: epoch, Busy: busy, Decisions: decisions}
}
