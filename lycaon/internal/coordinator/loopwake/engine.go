package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/promptresult"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	defaultCoordinatorWaitInterval = 2 * time.Minute
	// defaultWorkflowObligationInterval paces unmet workflow obligations.
	defaultWorkflowObligationInterval = 2 * time.Minute
)

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

// LoopEngine manages agent sleep/wake and coordinator host-initiated re-prompts.
type LoopEngine struct {
	depsMu    sync.RWMutex
	deps      LoopDeps
	waitStore *awaitstore.Store
	// ForgetSession releases every session-keyed store below.
	budget              sync.Map // loopBudgetKey -> int
	pendingDrain        sync.Map // sessionID -> struct{} (queue consumer)
	pendingQueues       sync.Map // sessionID -> *sessionNudgeQueue
	pendingWorkerQueues sync.Map // sessionID -> *deferredNudgeQueue
	kickDedup           sync.Map // loopKickKey -> loopKickStamp
	promptActive        sync.Map // sessionID -> struct{}
	promptExecution     sync.Map // sessionID -> *promptExecutionToken
	sleep               sessionSleeps
	asyncTurns          sync.WaitGroup // in-flight host turns
	asyncTurnsMu        sync.Mutex
	asyncTurnsBySession map[string]map[*asyncTurnWork]struct{}
	// nudgeSeq orders queued facts against prompt assembly.
	nudgeSeq          atomic.Uint64
	promptObservedSeq sync.Map // sessionID -> uint64 (facts observed by a completed model request)
	promptWorkflow    sync.Map // sessionID -> observedWorkflow
	waitWinners       sync.Map // sessionID -> *waitWinner
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

func NewLoopEngine() *LoopEngine {
	return &LoopEngine{}
}

func (l *LoopEngine) SetWaitStore(store *awaitstore.Store) {
	if l == nil {
		return
	}
	l.depsMu.Lock()
	l.waitStore = store
	l.depsMu.Unlock()
}

func (l *LoopEngine) durableWaitStore() *awaitstore.Store {
	l.depsMu.RLock()
	defer l.depsMu.RUnlock()
	return l.waitStore
}

// SetDeps refreshes loop dependencies.
func (l *LoopEngine) SetDeps(deps LoopDeps) {
	if l == nil {
		return
	}
	l.depsMu.Lock()
	l.deps = deps
	l.depsMu.Unlock()
}

func (l *LoopEngine) loopDeps() LoopDeps {
	if l == nil {
		return LoopDeps{}
	}
	l.depsMu.RLock()
	defer l.depsMu.RUnlock()
	return l.deps
}

// BeginPromptExecution marks model/tool execution active.
func (l *LoopEngine) BeginPromptExecution(ctx context.Context, sessionID string) func() {
	if l == nil {
		return func() {}
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return func() {}
	}
	token := &promptExecutionToken{}
	l.promptExecution.Store(sessionID, token)
	return func() {
		if !l.promptExecution.CompareAndDelete(sessionID, token) {
			return
		}
		l.schedulePendingDrain(context.WithoutCancel(ctx), sessionID, true)
	}
}

// PromptExecutionActive reports whether model/tool execution is active.
func (l *LoopEngine) PromptExecutionActive(sessionID string) bool {
	if l == nil {
		return false
	}
	_, ok := l.promptExecution.Load(strings.TrimSpace(sessionID))
	return ok
}

// BeginUserTurnSettlement orders settlement against host prompts.
func (l *LoopEngine) BeginUserTurnSettlement(ctx context.Context, sessionID string) (func(), bool) {
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return func() {}, false
	}
	if l.PromptExecutionActive(sessionID) || (!l.hostTurnBlocked(ctx, sessionID) && l.HasPendingLoopWakes(sessionID)) {
		return func() {}, false
	}
	if _, loaded := l.promptActive.LoadOrStore(sessionID, struct{}{}); loaded {
		return func() {}, false
	}
	if l.PromptExecutionActive(sessionID) || (!l.hostTurnBlocked(ctx, sessionID) && l.HasPendingLoopWakes(sessionID)) {
		l.releasePromptActiveAndRedrain(ctx, sessionID)
		return func() {}, false
	}
	return func() { l.releasePromptActiveAndRedrain(ctx, sessionID) }, true
}

// ResetBudget clears the per-run loop cycle counter.
func (l *LoopEngine) ResetBudget(runID string) {
	if l == nil || strings.TrimSpace(runID) == "" {
		return
	}
	l.budget.Range(func(key, _ any) bool {
		if budget, ok := key.(loopBudgetKey); ok && budget.runID == runID {
			l.budget.Delete(key)
		}
		return true
	})
}

// Nudge queues an optional inform and maybe runs a host coordinator turn.
func (l *LoopEngine) Nudge(ctx context.Context, sessionID string, wake, inform anchor.ID, legID string, env anchor.Envelope) {
	l.nudgeNow(ctx, sessionID, wake, inform, legID, "", env)
}

// NudgeAfterWorkerJobTerminal wakes the coordinator when a task() job completes.
func (l *LoopEngine) NudgeAfterWorkerJobTerminal(
	ctx context.Context,
	sessionID, completingJobID string,
	wake, inform anchor.ID,
	legID string,
	env anchor.Envelope,
) {
	l.nudgeNow(ctx, sessionID, wake, inform, legID, completingJobID, env)
}

func (l *LoopEngine) nudgeNow(
	ctx context.Context,
	sessionID string,
	wake, inform anchor.ID,
	legID, completingJobID string,
	env anchor.Envelope,
) {
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	seq := l.nudgeSeq.Add(1)
	pending := pendingLoopWake{wake: wake, inform: inform, legID: legID, completingJobID: completingJobID, env: env, seq: seq}
	if wake == anchor.PhaseAdvanced {
		if run, _, ok := l.activeRunAndVars(ctx, sessionID); ok && run != nil {
			pending.runID, pending.revision = run.ID, run.Revision
		}
	}
	l.deliverNudge(ctx, sessionID, pending)
}

func (l *LoopEngine) deliverNudge(ctx context.Context, sessionID string, pending pendingLoopWake) {
	wake, inform, legID, completingJobID, env := pending.wake, pending.inform, pending.legID, pending.completingJobID, pending.env
	deps := l.loopDeps()
	if l.resumePendingWait(ctx, sessionID, pending.wake) {
		return
	}
	// A subscribed result belongs to its lease, even after its originating batch closes.
	if l.matchesActiveWait(ctx, sessionID, pending) && l.routeWaitWake(ctx, sessionID, pending) {
		return
	}
	liveBatch := l.coordinatorBatchState(ctx, sessionID)
	if isStaleBatchSeq(liveBatch, env) {
		l.dropStaleBatchWake(ctx, sessionID, liveBatch)
		loopLogNudge(sessionID, wake, inform, legID, completingJobID, "drop:stale_batch_seq")
		return
	}
	if l.wakeConsumed(ctx, sessionID, pending) || l.routeWaitWake(ctx, sessionID, pending) {
		return
	}
	blocked := l.hostTurnBlocked(ctx, sessionID)
	// A budget request is actionable while its job is running.
	perJobWake := strings.TrimSpace(completingJobID) != "" || wake == anchor.WorkerBudgetRequested
	if !l.workerCycleIdle(ctx, sessionID, completingJobID) && !perJobWake {
		loopLogNudge(sessionID, wake, inform, legID, completingJobID, "defer_worker_cycle")
		l.deferNudge(ctx, sessionID, pending)
		return
	}
	runID := l.activeRunID(ctx, sessionID)
	allow, busy := l.evaluate(ctx, sessionID, wake)
	if !l.workerCycleIdle(ctx, sessionID, "") && !perJobWake {
		loopLogNudge(sessionID, wake, inform, legID, completingJobID, "defer_worker_cycle_idle")
		l.deferNudge(ctx, sessionID, pending)
		return
	}
	skipIn := HostWakeActionableInput{
		SessionID:       sessionID,
		Wake:            wake,
		Inform:          inform,
		CompletingJobID: completingJobID,
		Env:             env,
	}
	// Prompt execution defers actionability until the wake drains.
	if !busy && !blocked {
		if reason, skip := l.hostWakeSkipReason(ctx, skipIn); skip {
			loopLogNudge(sessionID, wake, inform, legID, completingJobID, reason)
			if reason == "skip_turn_rearm" {
				l.rearmSleepAfterSkip(ctx, sessionID)
			} else {
				// Rejected wakes preserve the active hold.
				l.parkForActiveHold(ctx, sessionID)
			}
			return
		}
	}
	dedupLegID := kickDedupLegID(inform, legID, completingJobID)
	shouldKick := !pending.informHandled && inform != "" && !l.recentKick(sessionID, runID, dedupLegID, wake)
	if shouldKick && inform == anchor.LegFinished && anchor.OmitInformWhenBoardReinjected(inform) &&
		deps.BoardWillForceInject != nil && deps.CoordinatorFrame != nil {
		if sess, err := deps.GetSession(ctx, sessionID); err == nil && sess != nil {
			frame, err := deps.CoordinatorFrame.BuildCoordinatorTurnFrame(ctx, sessionID, sess)
			if err == nil && deps.BoardWillForceInject(ctx, sess, frame.RunContext) {
				shouldKick = false
			}
		}
	}
	if shouldKick && deps.QueueInform != nil {
		emitEnv := env
		// A per-subject fact queues beside the same kick for other subjects.
		emitEnv.Subject = dedupLegID
		if liveBatch.Seq > 0 {
			emitEnv.BatchSeq = liveBatch.Seq
			emitEnv.BatchSeqSet = true
		}
		deps.QueueInform(ctx, sessionID, inform, emitEnv)
		l.markKick(sessionID, runID, dedupLegID, wake)
	}
	pending.informHandled = true
	if blocked {
		l.deferPromptWake(ctx, sessionID, pending)
		return
	}
	if !allow {
		loopLogNudge(sessionID, wake, inform, legID, completingJobID, "denied")
		// A denied wake leaves the active hold in place.
		l.parkForActiveHold(ctx, sessionID)
		return
	}
	if busy {
		if wake == anchor.WaitTimerFired {
			loopLogNudge(sessionID, wake, inform, legID, completingJobID, "drop:precedence_busy")
			return
		}
		loopLogNudge(sessionID, wake, inform, legID, completingJobID, "pending_busy")
		l.deferPromptWake(ctx, sessionID, pending)
		return
	}
	loopLogNudge(sessionID, wake, inform, legID, completingJobID, "queued_ready")
	if (wake == anchor.WorkerTaskFinished || wake == anchor.LegFinished) &&
		l.workerCycleIdle(ctx, sessionID, completingJobID) {
		l.DisarmTimerBackstop(ctx, sessionID)
	}
	l.enqueuePending(sessionID, pending)
	l.schedulePendingDrain(ctx, sessionID, false)
}

// settleDurableWaitWake routes a settled lease to its delivery owner.
func (l *LoopEngine) settleDurableWaitWake(
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
		CycleIdle:         l.workerCycleIdle(ctx, sessionID, completingJobID),
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
		l.breakSleep(ctx, sessionID, string(wake), true)
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
		l.rememberWaitWinner(sessionID, lease.ID, condition)
		l.breakSleep(ctx, sessionID, string(wake), false)
		l.runWaitResumeAsync(ctx, sessionID)
		return true
	}
	return false
}

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

// NudgeScanFinished deduplicates terminal scan wakes by scan id.
func (l *LoopEngine) NudgeScanFinished(ctx context.Context, sessionID, scanID string, env anchor.Envelope) {
	l.Nudge(ctx, sessionID, anchor.ScanFinished, anchor.ScanFinished, scanID, env)
}

// NudgeProcessFinished deduplicates process wakes by handle.
func (l *LoopEngine) NudgeProcessFinished(ctx context.Context, sessionID, handle string, env anchor.Envelope) {
	l.Nudge(ctx, sessionID, anchor.ProcessFinished, anchor.ProcessFinished, handle, env)
}

// NudgeProcessRefused wakes a wait on a running job the kernel refused.
func (l *LoopEngine) NudgeProcessRefused(ctx context.Context, sessionID, handle string, env anchor.Envelope) {
	l.Nudge(ctx, sessionID, anchor.ProcessRefused, anchor.ProcessRefused, handle, env)
}

// NudgeWorkerBudgetRequested wakes the coordinator that owns a worker's budget
// request; the request is durable on the job, so one wake per request suffices.
func (l *LoopEngine) NudgeWorkerBudgetRequested(ctx context.Context, sessionID, jobID string, env anchor.Envelope) {
	l.Nudge(ctx, sessionID, anchor.WorkerBudgetRequested, anchor.WorkerBudgetRequested, jobID, env)
}

// NudgeLegFinished is called when a delegation worker leg completes.
func (l *LoopEngine) NudgeLegFinished(ctx context.Context, parentID string, completedAt time.Time, legID string) {
	env := anchor.Envelope{}
	if !completedAt.IsZero() {
		t := completedAt
		env.CompletedAt = &t
	}
	// The kick names the leg so a recall for omitted detail can address it.
	if id := strings.TrimSpace(legID); id != "" {
		env.Vars = map[string]any{"leg_id": id}
	}
	l.Nudge(ctx, parentID, anchor.LegFinished, anchor.LegFinished, legID, env)
}

// ClearPending drops deferred loop wakes.
func (l *LoopEngine) ClearPending(sessionID string) {
	if l == nil {
		return
	}
	l.pendingQueues.Delete(sessionID)
}

// DrainPending runs deferred loop wakes after Prompt completes.
func (l *LoopEngine) DrainPending(ctx context.Context, sessionID string) {
	if l == nil {
		return
	}
	l.drainPending(context.WithoutCancel(ctx), sessionID, false)
}

func (l *LoopEngine) notifyLoopQuiescent(ctx context.Context, sessionID string) {
	if l == nil || (!l.hostTurnBlocked(ctx, sessionID) && (l.HasPendingLoopWakes(sessionID) || !l.workerCycleIdle(ctx, sessionID, ""))) {
		return
	}
	if notify := l.loopDeps().OnLoopQuiescent; notify != nil {
		notify(ctx, sessionID)
	}
}

// InterruptSleep clears sleep and stale deferred wakes after a visible prompt.
func (l *LoopEngine) InterruptSleep(ctx context.Context, sessionID string) {
	if store := l.durableWaitStore(); store != nil {
		_ = store.InterruptSession(ctx, sessionID, "interrupted")
	}
	l.waitWinners.Delete(strings.TrimSpace(sessionID))
	l.breakSleep(ctx, sessionID, "user_prompt", false)
	l.ClearPending(sessionID)
}

// EnterSleep arms a host-owned park; host parks are never completion waits.
func (l *LoopEngine) EnterSleep(
	ctx context.Context,
	sessionID string,
	until time.Time,
	reason string,
	triggers []WaitTrigger,
	processHandles []string,
	mover SleepMover,
) {
	l.enterSleep(ctx, sessionID, sleepArm{
		until: until, reason: reason, triggers: triggers, processHandles: processHandles, mover: mover,
	})
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

func (l *LoopEngine) enterSleep(ctx context.Context, sessionID string, arm sleepArm) {
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	triggers := arm.triggers
	if triggers == nil {
		triggers = DefaultCoordinatorWaitTriggers(l.overlayPromoteDue(ctx, sessionID, anchor.Envelope{}))
	}
	st := l.sleep.state(sessionID)
	// Preserve context values for the later timer wake.
	wakeCtx := context.WithoutCancel(ctx)

	st.mu.Lock()
	// A re-arm retires the previous lease before opening another.
	closed, hadLease := closeWaitLeaseLocked(st, sessionID)
	cancelSleepTimerLocked(st)
	st.armed = true
	st.untilComplete = arm.untilComplete
	st.until = arm.until.UTC()
	st.interruptedUntil = time.Time{}
	st.reason = strings.TrimSpace(arm.reason)
	st.waitTriggers = dedupeWaitTriggers(triggers)
	st.processHandles = normalizeProcessHandles(arm.processHandles)
	st.workerHandles = normalizeProcessHandles(arm.workerHandles)
	st.mover = arm.mover
	opened := openWaitLeaseLocked(st, sessionID, arm.mover)
	loopLogSleep(sessionID, "arm", arm.reason, st.until)
	l.armSleepTimerLocked(st, wakeCtx, sessionID, arm.reason)
	st.mu.Unlock()

	if hadLease {
		l.publishWaitLease(ctx, closed)
	}
	if opened.ActivityID != "" {
		l.publishWaitLease(ctx, opened)
	}
}

// armSleepTimerLocked arms only explicit timer subscriptions.
func (l *LoopEngine) armSleepTimerLocked(st *sessionSleep, wakeCtx context.Context, sessionID, reason string) {
	if _, ok := waitTriggerSet(st.waitTriggers)[WaitTriggerTimer]; !ok {
		st.timer = nil
		return
	}
	remaining := time.Until(st.until)
	if remaining <= 0 {
		remaining = time.Millisecond
	}
	generation := st.timerGeneration
	timerDone := make(chan struct{})
	st.timerDone = timerDone
	st.timer = time.AfterFunc(remaining, func() {
		defer close(timerDone)
		st.mu.Lock()
		if st.timerGeneration != generation {
			st.mu.Unlock()
			return
		}
		until := st.until
		st.timer = nil
		st.timerGeneration++
		// The deadline also retires the activity lease.
		expired, hadLease := closeWaitLeaseLocked(st, sessionID)
		st.mu.Unlock()
		if store := l.durableWaitStore(); store != nil {
			winner := awaitstore.Condition{Kind: "timer", Outcome: "timed_out"}
			if lease, active, _ := store.ForSession(wakeCtx, sessionID); active {
				if won, _ := store.SettleLease(wakeCtx, lease.ID, "timed_out", winner); won {
					l.rememberWaitWinner(sessionID, lease.ID, winner)
				}
			}
		}
		if hadLease {
			l.publishWaitLease(wakeCtx, expired)
		}
		loopLogSleep(sessionID, "timer_fire", reason, until)
		l.Nudge(wakeCtx, sessionID, anchor.WaitTimerFired, anchor.WaitTimerFired, "", anchor.Envelope{})
	})
}

// MarkWaitCalled records that wait() ended the current cycle.
func (l *LoopEngine) MarkWaitCalled(sessionID string) {
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	st.waitThisTurn = true
	st.mu.Unlock()
}

// parkForActiveHold restores the park broken before wake evaluation.
func (l *LoopEngine) parkForActiveHold(ctx context.Context, sessionID string) bool {
	if l == nil {
		return false
	}
	if l.sessionHasPendingUserInput(ctx, sessionID) {
		l.ParkForPendingUserInput(ctx, sessionID, "pending user ask")
		return true
	}
	maxSleep := time.Now().UTC().Add(l.sessionLimits(ctx, sessionID).CoordinatorMaxSleep())
	overlayPromoteDue := l.overlayPromoteDue(ctx, sessionID, anchor.Envelope{})
	if l.sessionHumanApprovalAwaiting(ctx, sessionID) {
		l.EnterSleep(
			ctx,
			sessionID,
			maxSleep,
			"awaiting human approval",
			AwaitUserWaitTriggers(overlayPromoteDue),
			nil,
			SleepMoverUser,
		)
		return true
	}
	// The host observer owns this phase's wake.
	if l.sessionHostObligationHeld(ctx, sessionID) {
		l.EnterSleep(
			ctx,
			sessionID,
			maxSleep,
			l.hostObligationParkReason(ctx, sessionID),
			HostObligationWaitTriggers(overlayPromoteDue),
			nil,
			SleepMoverHost,
		)
		return true
	}
	return false
}

// OnTurnComplete selects sleep policy and visible-turn continuation.
func (l *LoopEngine) OnTurnComplete(ctx context.Context, sessionID string, hostTurn bool) UserTurnContinuation {
	if l == nil {
		return UserTurnSettled
	}
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	waited := st.waitThisTurn
	st.waitThisTurn = false
	st.mu.Unlock()
	if waited {
		loopLogSleep(sessionID, "turn_complete_wait", "wait() called", time.Time{})
		return UserTurnContinues
	}
	if l.parkForActiveHold(ctx, sessionID) {
		return UserTurnContinues
	}
	cycleIdle := l.WorkerCycleIsIdle(ctx, sessionID)
	// Unmet phase obligations re-arm a workflow wake instead of the awaiting-user park.
	if cycleIdle && l.workflowObligationsOpen(ctx, sessionID) {
		overlayPromoteDue := l.overlayPromoteDue(ctx, sessionID, anchor.Envelope{})
		l.EnterSleep(
			ctx,
			sessionID,
			time.Now().UTC().Add(defaultWorkflowObligationInterval),
			"workflow obligations open",
			WorkflowObligationWaitTriggers(overlayPromoteDue),
			nil,
			SleepMoverHost,
		)
		return UserTurnContinues
	}
	if hostTurn && cycleIdle {
		overlayPromoteDue := l.overlayPromoteDue(ctx, sessionID, anchor.Envelope{})
		l.EnterSleep(
			ctx,
			sessionID,
			time.Now().UTC().Add(l.sessionLimits(ctx, sessionID).CoordinatorMaxSleep()),
			"awaiting user after idle host turn",
			AwaitUserWaitTriggers(overlayPromoteDue),
			nil,
			SleepMoverUser,
		)
		return UserTurnSettled
	}
	if hostTurn {
		loopLogSleep(sessionID, "turn_complete_no_park", "host turn without wait()", time.Time{})
	}
	if !cycleIdle {
		return UserTurnContinues
	}
	return UserTurnSettled
}

func (l *LoopEngine) breakSleep(ctx context.Context, sessionID, reason string, preserveDeadline bool) {
	if l == nil {
		return
	}
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	cancelSleepTimerLocked(st)
	if !st.until.IsZero() {
		loopLogSleep(sessionID, "break", reason, st.until)
	}
	if !preserveDeadline {
		st.interruptedUntil = time.Time{}
	} else if !st.until.IsZero() && st.until.After(time.Now().UTC()) {
		st.interruptedUntil = st.until.UTC()
	}
	st.armed = false
	st.until = time.Time{}
	st.reason = ""
	closed, hadLease := closeWaitLeaseLocked(st, sessionID)
	st.mu.Unlock()
	if hadLease {
		l.publishWaitLease(ctx, closed)
	}
}

// ResolveWaitUntil restores an interrupted deadline when requested.
func (l *LoopEngine) ResolveWaitUntil(ctx context.Context, sessionID string, resume bool, requested time.Duration) (until time.Time, resumed bool) {
	now := time.Now().UTC()
	if !resume {
		return now.Add(l.capSleepDuration(ctx, sessionID, requested)), false
	}
	if requested <= 0 {
		requested = defaultCoordinatorWaitInterval
	}
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return now.Add(l.capSleepDuration(ctx, sessionID, requested)), false
	}
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	interrupted := st.interruptedUntil
	st.interruptedUntil = time.Time{}
	st.mu.Unlock()
	if !interrupted.IsZero() && interrupted.After(now) {
		return interrupted, true
	}
	return now.Add(l.capSleepDuration(ctx, sessionID, requested)), false
}

// InterruptedUntilForTest returns the stashed sleep deadline after an early nudge wake.
func (l *LoopEngine) InterruptedUntilForTest(sessionID string) time.Time {
	if l == nil {
		return time.Time{}
	}
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.interruptedUntil
}

// ForgetSession releases coordinator state that cannot be observed after deletion.
func (l *LoopEngine) ForgetSession(ctx context.Context, sessionID string) {
	if l == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	if store := l.durableWaitStore(); store != nil {
		_ = store.InterruptSession(ctx, sessionID, "canceled")
	}
	l.waitWinners.Delete(sessionID)
	// Drain turns before clearing state that late wakes could recreate.
	l.cancelAndDrainAsyncTurns(sessionID)
	for {
		value, ok := l.sleep.LoadAndDelete(sessionID)
		if !ok {
			break
		}
		if st, valid := value.(*sessionSleep); valid && st != nil {
			st.mu.Lock()
			timerDone := st.timerDone
			cancelSleepTimerLocked(st)
			st.waitTriggers = nil
			st.processHandles = nil
			st.workerHandles = nil
			closed, hadLease := closeWaitLeaseLocked(st, sessionID)
			st.mu.Unlock()
			if hadLease {
				l.publishWaitLease(ctx, closed)
			}
			if timerDone != nil {
				<-timerDone
			}
		}
	}
	l.pendingQueues.Delete(sessionID)
	l.pendingDrain.Delete(sessionID)
	l.pendingWorkerQueues.Delete(sessionID)
	l.promptActive.Delete(sessionID)
	l.promptExecution.Delete(sessionID)
	l.promptObservedSeq.Delete(sessionID)
	l.promptWorkflow.Delete(sessionID)
	l.waitWinners.Delete(sessionID)
	l.budget.Range(func(key, _ any) bool {
		if budget, ok := key.(loopBudgetKey); ok && budget.sessionID == sessionID {
			l.budget.Delete(key)
		}
		return true
	})
	l.kickDedup.Range(func(key, _ any) bool {
		if kick, ok := key.(loopKickKey); ok && kick.sessionID == sessionID {
			l.kickDedup.Delete(key)
		}
		return true
	})
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

// SleepReasonForTest returns the armed sleep reason for tests.
func (l *LoopEngine) SleepReasonForTest(sessionID string) string {
	if l == nil {
		return ""
	}
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.reason
}

// SleepUntilForTest returns the armed sleep deadline for tests.
func (l *LoopEngine) SleepUntilForTest(sessionID string) time.Time {
	if l == nil {
		return time.Time{}
	}
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.until
}

// IsSleeping reports an armed wait. Event-only waits require an explicit break.
func (l *LoopEngine) IsSleeping(sessionID string) bool {
	if l == nil {
		return false
	}
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return sleepArmedLocked(st, time.Now())
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

// ShouldLoopWake exposes policy evaluation for tests.
func (l *LoopEngine) ShouldLoopWake(ctx context.Context, sessionID string, wake anchor.ID) (bool, string, error) {
	if l.hostTurnBlocked(ctx, sessionID) {
		return false, "execution_failed", nil
	}
	allow, busy := l.evaluate(ctx, sessionID, wake)
	if busy {
		return false, "session_busy", nil
	}
	if !allow {
		deps := l.loopDeps()
		sess, err := deps.GetSession(ctx, sessionID)
		if err != nil {
			return false, "", err
		}
		if !deps.Limits(ctx, sess).CoordinatorLoopEnabled() {
			return false, "feature_disabled", nil
		}
		run, vars, ok := l.activeRunAndVars(ctx, sessionID)
		if !ok || run == nil {
			return false, "no_active_run", nil
		}
		if l.sessionHumanApprovalAwaiting(ctx, sessionID) {
			return false, "human_approval_awaiting", nil
		}
		if l.sessionHostObligationHeld(ctx, sessionID) {
			return false, "host_obligation_held", nil
		}
		if scaffoldvars.HasPendingUserInput(vars) {
			return false, "pending_user_input", nil
		}
		return false, "denied", nil
	}
	return true, "", nil
}

// EvaluateForTest exposes loop wake gating for integration tests.
func (l *LoopEngine) EvaluateForTest(ctx context.Context, sessionID string, wake anchor.ID) (allow bool, busy bool) {
	return l.evaluate(ctx, sessionID, wake)
}

func (l *LoopEngine) evaluate(ctx context.Context, sessionID string, wake anchor.ID) (allow bool, busy bool) {
	if l.hostTurnBlocked(ctx, sessionID) {
		return false, false
	}
	deps := l.loopDeps()
	sess, err := deps.GetSession(ctx, sessionID)
	if err != nil || sess == nil {
		return false, false
	}
	if !deps.Limits(ctx, sess).CoordinatorLoopEnabled() {
		return false, false
	}
	if deps.IsCoordinatorSession != nil && !deps.IsCoordinatorSession(ctx, sess) {
		return false, false
	}
	if deps.IsEscalated != nil && deps.IsEscalated(sessionID) {
		return false, false
	}
	if l.PromptExecutionActive(sessionID) {
		return true, true
	}
	run, vars, ok := l.activeRunAndVars(ctx, sessionID)
	if !ok || run == nil {
		return false, false
	}
	if run.Status != api.WorkflowRunStatusRunning {
		return false, false
	}
	if l.sessionHumanApprovalAwaiting(ctx, sessionID) {
		return false, false
	}
	if l.sessionHostObligationHeld(ctx, sessionID) {
		return false, false
	}
	if scaffoldvars.HasPendingUserInput(vars) {
		return false, false
	}
	return true, false
}

func (l *LoopEngine) workflowObligationsOpen(ctx context.Context, sessionID string) bool {
	if l == nil {
		return false
	}
	deps := l.loopDeps()
	if deps.WorkflowObligationsOpen == nil {
		return false
	}
	return deps.WorkflowObligationsOpen(ctx, sessionID)
}

// Read failures do not infer an approval park.
func (l *LoopEngine) sessionHumanApprovalAwaiting(ctx context.Context, sessionID string) bool {
	if l == nil {
		return false
	}
	deps := l.loopDeps()
	if deps.WorkflowSource == nil {
		return false
	}
	awaiting, err := deps.WorkflowSource.Approvals.HumanApprovalAwaiting(ctx, sessionID)
	if err != nil {
		slog.WarnContext(ctx, "human approval park unreadable; not inferring a park",
			"component", "coordinator_loop", "session_id", sessionID, "error", err)
		return false
	}
	return awaiting
}

// Read failures do not infer a host-obligation hold.
func (l *LoopEngine) sessionHostObligationHeld(ctx context.Context, sessionID string) bool {
	if l == nil {
		return false
	}
	deps := l.loopDeps()
	if deps.WorkflowSource == nil {
		return false
	}
	held, err := deps.WorkflowSource.Obligations.HostObligationHeld(ctx, sessionID)
	if err != nil {
		slog.WarnContext(ctx, "host obligation ledger unreadable; not inferring a hold",
			"component", "coordinator_loop", "session_id", sessionID, "error", err)
		return false
	}
	return held
}

// hostObligationParkReason names the holding kinds in the sleep ledger.
func (l *LoopEngine) hostObligationParkReason(ctx context.Context, sessionID string) string {
	const base = "awaiting host obligation"
	if l == nil {
		return base
	}
	deps := l.loopDeps()
	if deps.WorkflowSource == nil {
		return base
	}
	kinds := deps.WorkflowSource.Obligations.HostObligationHoldKinds(ctx, sessionID)
	if len(kinds) == 0 {
		return base
	}
	return base + ": " + strings.Join(kinds, ", ")
}

func (l *LoopEngine) tryConsumeBudget(ctx context.Context, sessionID, runID string, wake anchor.ID) bool {
	if strings.TrimSpace(runID) == "" {
		run, _, ok := l.activeRunAndVars(ctx, sessionID)
		if !ok || run == nil {
			return false
		}
		runID = run.ID
	}
	deps := l.loopDeps()
	sess, err := deps.GetSession(ctx, sessionID)
	if err != nil || sess == nil {
		return false
	}
	max := deps.Limits(ctx, sess).EffectiveMaxCoordinatorLoopCycles()
	var count int
	key := loopBudgetKey{sessionID: sessionID, runID: runID}
	if v, ok := l.budget.Load(key); ok {
		count, _ = v.(int)
	}
	if count >= max {
		loopLogBudget(sessionID, runID, count, max, false)
		return false
	}
	l.budget.Store(key, count+1)
	loopLogBudget(sessionID, runID, count+1, max, true)
	return true
}

func (l *LoopEngine) runPromptSync(ctx context.Context, sessionID string, pending pendingLoopWake) bool {
	if l.hostTurnBlocked(ctx, sessionID) {
		l.enqueuePending(sessionID, pending)
		return false
	}
	if _, loaded := l.promptActive.LoadOrStore(sessionID, struct{}{}); loaded {
		loopLogNudge(sessionID, pending.wake, pending.inform, pending.legID, pending.completingJobID, "prompt_active_requeue")
		l.deferPromptWake(ctx, sessionID, pending)
		return false
	}
	defer l.releasePromptActiveAndRedrain(ctx, sessionID)
	wake := pending.wake
	if l.wakeConsumed(ctx, sessionID, pending) {
		return true
	}
	if l.PromptExecutionActive(sessionID) {
		loopLogNudge(sessionID, wake, pending.inform, pending.legID, pending.completingJobID, "sync_defer_busy")
		l.deferPromptWake(ctx, sessionID, pending)
		return false
	}
	// Explicit wait delivery precedes optional workflow gates.
	if l.routeWaitWake(ctx, sessionID, pending) {
		return true
	}
	allow, busy := l.evaluate(ctx, sessionID, wake)
	if busy {
		l.deferPromptWake(ctx, sessionID, pending)
		return false
	}
	if !allow {
		return true
	}
	if pending.completingJobID == "" && wake != anchor.WorkerBudgetRequested && !pending.env.HasWorkerDecision() && !l.workerCycleIdle(ctx, sessionID, "") {
		l.deferNudge(ctx, sessionID, pending)
		return true
	}
	if reason, skip := l.hostWakeSkipReason(ctx, pending.actionableInput(sessionID)); skip {
		loopLogNudge(sessionID, wake, pending.inform, pending.legID, pending.completingJobID, "drain_"+reason)
		l.parkForActiveHold(ctx, sessionID)
		return true
	}

	runID := l.activeRunID(ctx, sessionID)
	if !l.tryConsumeBudget(ctx, sessionID, runID, wake) {
		loopLogNudge(sessionID, wake, "", "", "", "budget_exhausted")
		return false
	}
	loopLogRunPrompt(sessionID, wake)
	deps := l.loopDeps()
	if deps.RunPrompt != nil {
		_, _ = deps.RunPrompt(ctx, sessionID)
	}
	return true
}

// releasePromptActiveAndRedrain releases the host lane and drains queued wakes.
func (l *LoopEngine) releasePromptActiveAndRedrain(ctx context.Context, sessionID string) {
	l.promptActive.Delete(sessionID)
	l.schedulePendingDrain(ctx, sessionID, true)
}

// spawnAsyncTurn launches a tracked, cancelable host turn.
func (l *LoopEngine) spawnAsyncTurn(ctx context.Context, sessionID string, fn func(ctx context.Context)) {
	timeout := l.sessionLimits(ctx, sessionID).CoordinatorHostTurnTimeout()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	work := l.registerAsyncTurn(sessionID, cancel)
	l.asyncTurns.Add(1)
	go func() {
		defer l.asyncTurns.Done()
		defer l.finishAsyncTurn(sessionID, work)
		defer observability.GuardPanic("coordinator.loopwake.async_turn")
		fn(ctx)
	}()
}

// registerAsyncTurn makes the turn reachable by cleanup.
func (l *LoopEngine) registerAsyncTurn(sessionID string, cancel context.CancelFunc) *asyncTurnWork {
	work := &asyncTurnWork{cancel: cancel, done: make(chan struct{})}
	l.asyncTurnsMu.Lock()
	if l.asyncTurnsBySession == nil {
		l.asyncTurnsBySession = make(map[string]map[*asyncTurnWork]struct{})
	}
	if l.asyncTurnsBySession[sessionID] == nil {
		l.asyncTurnsBySession[sessionID] = make(map[*asyncTurnWork]struct{})
	}
	l.asyncTurnsBySession[sessionID][work] = struct{}{}
	l.asyncTurnsMu.Unlock()
	return work
}

func (l *LoopEngine) finishAsyncTurn(sessionID string, work *asyncTurnWork) {
	if work == nil {
		return
	}
	l.asyncTurnsMu.Lock()
	delete(l.asyncTurnsBySession[sessionID], work)
	if len(l.asyncTurnsBySession[sessionID]) == 0 {
		delete(l.asyncTurnsBySession, sessionID)
	}
	close(work.done)
	l.asyncTurnsMu.Unlock()
	work.cancel()
}

// cancelAndDrainAsyncTurns stops one session's host turns.
func (l *LoopEngine) cancelAndDrainAsyncTurns(sessionID string) {
	l.asyncTurnsMu.Lock()
	works := make([]*asyncTurnWork, 0, len(l.asyncTurnsBySession[sessionID]))
	for work := range l.asyncTurnsBySession[sessionID] {
		works = append(works, work)
		work.cancel()
	}
	l.asyncTurnsMu.Unlock()
	for _, work := range works {
		<-work.done
	}
}

// cancelAllAsyncTurns force-cancels every session's in-flight async turns.
func (l *LoopEngine) cancelAllAsyncTurns() {
	l.asyncTurnsMu.Lock()
	var cancels []context.CancelFunc
	for _, set := range l.asyncTurnsBySession {
		for work := range set {
			cancels = append(cancels, work.cancel)
		}
	}
	l.asyncTurnsMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// WaitForAsyncTurns drains host turns before resources close.
func (l *LoopEngine) WaitForAsyncTurns(ctx context.Context) {
	if l == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		l.asyncTurns.Wait()
		close(done)
	}()
	select {
	case <-done:
		return
	case <-ctx.Done():
	}
	l.cancelAllAsyncTurns()
	<-done
}

func (l *LoopEngine) capSleepDuration(ctx context.Context, sessionID string, requested time.Duration) time.Duration {
	return CapWaitDuration(requested, l.sessionLimits(ctx, sessionID).CoordinatorMaxSleep())
}

func (l *LoopEngine) sessionLimits(ctx context.Context, sessionID string) settings.SessionLimits {
	if l == nil {
		return settings.DefaultSessionLimits()
	}
	deps := l.loopDeps()
	if deps.GetSession == nil || deps.Limits == nil {
		return settings.DefaultSessionLimits()
	}
	sess, err := deps.GetSession(ctx, sessionID)
	if err != nil || sess == nil {
		return settings.DefaultSessionLimits()
	}
	return deps.Limits(ctx, sess)
}

func (l *LoopEngine) activeRunAndVars(ctx context.Context, sessionID string) (*api.WorkflowRun, map[string]any, bool) {
	deps := l.loopDeps()
	if deps.WorkflowSource == nil {
		return nil, nil, false
	}
	run, err := deps.WorkflowSource.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || run == nil {
		return nil, nil, false
	}
	vars, err := deps.WorkflowSource.Runs.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return run, nil, true
	}
	return run, vars, true
}

func (l *LoopEngine) activeRunID(ctx context.Context, sessionID string) string {
	run, _, ok := l.activeRunAndVars(ctx, sessionID)
	if !ok || run == nil {
		return ""
	}
	return run.ID
}

func kickDedupLegID(inform anchor.ID, legID, completingJobID string) string {
	if inform == anchor.WorkerTaskFinished {
		if jobID := strings.TrimSpace(completingJobID); jobID != "" {
			return jobID
		}
	}
	return legID
}

func (l *LoopEngine) recentKick(sessionID, runID, legID string, wake anchor.ID) bool {
	if strings.TrimSpace(runID) == "" {
		return false
	}
	key := loopKickKey{sessionID: sessionID, runID: runID, legID: legID, wake: wake}
	if v, ok := l.kickDedup.Load(key); ok {
		if stamp, ok := v.(loopKickStamp); ok && time.Since(stamp.at) < 30*time.Second {
			return true
		}
	}
	return false
}

func (l *LoopEngine) markKick(sessionID, runID, legID string, wake anchor.ID) {
	if strings.TrimSpace(runID) == "" {
		return
	}
	key := loopKickKey{sessionID: sessionID, runID: runID, legID: legID, wake: wake}
	l.kickDedup.Store(key, loopKickStamp{at: time.Now().UTC()})
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

// TryConsumeBudgetForTest exposes budget consumption for unit tests.
func (l *LoopEngine) TryConsumeBudgetForTest(ctx context.Context, sessionID, runID string) bool {
	return l.tryConsumeBudget(ctx, sessionID, runID, anchor.LegFinished)
}

// PendingForTest reports a deferred loop wake.
func (l *LoopEngine) PendingForTest(sessionID string) (anchor.ID, bool) {
	if l == nil {
		return "", false
	}
	pending, ok := l.sessionPendingQueue(sessionID).peek()
	return pending.wake, ok
}

func (l *LoopEngine) sessionHasPendingUserInput(ctx context.Context, sessionID string) bool {
	_, vars, ok := l.activeRunAndVars(ctx, sessionID)
	if !ok || vars == nil {
		return false
	}
	return scaffoldvars.HasPendingUserInput(vars)
}

// pendingUserInputWaitDeadline bounds an unanswered coordinator ask.
func (l *LoopEngine) pendingUserInputWaitDeadline(ctx context.Context, sessionID string) time.Time {
	return time.Now().UTC().Add(l.sessionLimits(ctx, sessionID).CoordinatorMaxSleep())
}
