package loopwake

import (
	"context"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

type AdmissionDeps struct {
	GetSession           func(ctx context.Context, sessionID string) (*api.Session, error)
	IsCoordinatorSession func(ctx context.Context, sess *api.Session) bool
	IsEscalated          func(sessionID string) bool
	Limits               func(context.Context, *api.Session) settings.SessionLimits
}
type Admission struct {
	depsMu          sync.RWMutex
	deps            AdmissionDeps
	budget          sync.Map
	promptExecution sync.Map
	Turns           *HostTurns
	Nudges          *Nudges
	Facts           *SessionFacts
}

func (l *Admission) setDeps(deps AdmissionDeps) { l.depsMu.Lock(); l.deps = deps; l.depsMu.Unlock() }
func (l *Admission) loopDeps() AdmissionDeps {
	if l == nil {
		return AdmissionDeps{}
	}
	l.depsMu.RLock()
	defer l.depsMu.RUnlock()
	return l.deps
}
func (l *Admission) BeginPromptExecution(ctx context.Context, sessionID string) func() {
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
		l.Nudges.schedulePendingDrain(context.WithoutCancel(ctx), sessionID, true)
	}
}
func (l *Admission) PromptExecutionActive(sessionID string) bool {
	if l == nil {
		return false
	}
	_, ok := l.promptExecution.Load(strings.TrimSpace(sessionID))
	return ok
}

func (l *Admission) ShouldLoopWake(ctx context.Context, sessionID string, wake anchor.ID) (bool, string, error) {
	_, reason, err := l.decision(ctx, sessionID)
	switch reason {
	case api.WakeReasonTurnBlocked:
		return false, "execution_failed", err
	case api.WakeReasonPromptActive:
		return false, "session_busy", err
	case api.WakeReasonLoopDisabled:
		return false, "feature_disabled", err
	default:
		return reason == "", string(reason), err
	}
}
func (l *Admission) decision(ctx context.Context, sessionID string) (api.WakeState, api.WakeReason, error) {
	if l.Turns.hostTurnBlocked(ctx, sessionID) {
		return api.WakeStateDeferred, api.WakeReasonTurnBlocked, nil
	}
	deps := l.loopDeps()
	sess, err := deps.GetSession(ctx, sessionID)
	if err != nil {
		return api.WakeStateDropped, api.WakeReasonLookupFailed, err
	}
	if sess == nil {
		return api.WakeStateDropped, api.WakeReasonSessionMissing, nil
	}
	if !deps.Limits(ctx, sess).CoordinatorLoopEnabled() {
		return api.WakeStateDenied, api.WakeReasonLoopDisabled, nil
	}
	if deps.IsCoordinatorSession != nil && !deps.IsCoordinatorSession(ctx, sess) {
		return api.WakeStateDenied, api.WakeReasonWrongSessionRole, nil
	}
	if deps.IsEscalated != nil && deps.IsEscalated(sessionID) {
		return api.WakeStateDenied, api.WakeReasonEscalated, nil
	}
	if l.PromptExecutionActive(sessionID) {
		return api.WakeStateDeferred, api.WakeReasonPromptActive, nil
	}
	source := l.Facts.loopDeps().WorkflowSource
	if source == nil {
		return api.WakeStateDropped, api.WakeReasonNoActiveRun, nil
	}
	run, err := source.Runs.ActiveBySession(ctx, sessionID)
	if err != nil {
		return api.WakeStateDropped, api.WakeReasonLookupFailed, err
	}
	if run == nil || run.Status != api.WorkflowRunStatusRunning {
		return api.WakeStateDropped, api.WakeReasonNoActiveRun, nil
	}
	if l.Facts.sessionHumanApprovalAwaiting(ctx, sessionID) {
		return api.WakeStateHeld, api.WakeReasonHumanApprovalAwaiting, nil
	}
	if l.Facts.sessionHostObligationHeld(ctx, sessionID) {
		return api.WakeStateHeld, api.WakeReasonHostObligationHeld, nil
	}
	vars, err := source.Runs.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return api.WakeStateDropped, api.WakeReasonLookupFailed, err
	}
	if scaffoldvars.HasPendingUserInput(vars) {
		return api.WakeStateHeld, api.WakeReasonPendingUserInput, nil
	}
	return "", "", nil
}
func (l *Admission) ConsumeBudget(ctx context.Context, sessionID, runID string) api.WakeReason {
	if runID == "" {
		source := l.Facts.loopDeps().WorkflowSource
		if source == nil {
			return api.WakeReasonNoActiveRun
		}
		run, err := source.Runs.ActiveBySession(ctx, sessionID)
		if err != nil {
			return api.WakeReasonLookupFailed
		}
		if run == nil {
			return api.WakeReasonNoActiveRun
		}
		runID = run.ID
	}
	deps := l.loopDeps()
	sess, err := deps.GetSession(ctx, sessionID)
	if err != nil {
		return api.WakeReasonLookupFailed
	}
	if sess == nil {
		return api.WakeReasonSessionMissing
	}
	max := deps.Limits(ctx, sess).EffectiveMaxCoordinatorLoopCycles()
	key := loopBudgetKey{sessionID: sessionID, runID: runID}
	for {
		value, _ := l.budget.LoadOrStore(key, 0)
		count := value.(int)
		if count >= max {
			loopLogBudget(sessionID, runID, count, max, false)
			return api.WakeReasonBudgetExhausted
		}
		if l.budget.CompareAndSwap(key, count, count+1) {
			loopLogBudget(sessionID, runID, count+1, max, true)
			return ""
		}
	}
}

type loopBudgetKey struct {
	sessionID string
	runID     string
}

// Nonzero size keeps execution tokens distinct.
type promptExecutionToken struct{ _ byte }
