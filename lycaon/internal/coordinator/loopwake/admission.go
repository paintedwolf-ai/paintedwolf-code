package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"sync"
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
func (l *Admission) BeginUserTurnSettlement(ctx context.Context, sessionID string) (func(), bool) {
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return func() {}, false
	}
	if l.PromptExecutionActive(sessionID) || (!l.Turns.hostTurnBlocked(ctx, sessionID) && l.Nudges.HasPendingLoopWakes(sessionID)) {
		return func() {}, false
	}
	if _, loaded := l.Turns.promptActive.LoadOrStore(sessionID, struct{}{}); loaded {
		return func() {}, false
	}
	if l.PromptExecutionActive(sessionID) || (!l.Turns.hostTurnBlocked(ctx, sessionID) && l.Nudges.HasPendingLoopWakes(sessionID)) {
		l.Turns.releasePromptActiveAndRedrain(ctx, sessionID)
		return func() {}, false
	}
	return func() { l.Turns.releasePromptActiveAndRedrain(ctx, sessionID) }, true
}
func (l *Admission) ResetBudget(runID string) {
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
func (l *Admission) ShouldLoopWake(ctx context.Context, sessionID string, wake anchor.ID) (bool, string, error) {
	if l.Turns.hostTurnBlocked(ctx, sessionID) {
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
		run, vars, ok := l.Facts.activeRunAndVars(ctx, sessionID)
		if !ok || run == nil {
			return false, "no_active_run", nil
		}
		if l.Facts.sessionHumanApprovalAwaiting(ctx, sessionID) {
			return false, "human_approval_awaiting", nil
		}
		if l.Facts.sessionHostObligationHeld(ctx, sessionID) {
			return false, "host_obligation_held", nil
		}
		if scaffoldvars.HasPendingUserInput(vars) {
			return false, "pending_user_input", nil
		}
		return false, "denied", nil
	}
	return true, "", nil
}
func (l *Admission) evaluate(ctx context.Context, sessionID string, wake anchor.ID) (allow bool, busy bool) {
	if l.Turns.hostTurnBlocked(ctx, sessionID) {
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
	run, vars, ok := l.Facts.activeRunAndVars(ctx, sessionID)
	if !ok || run == nil {
		return false, false
	}
	if run.Status != api.WorkflowRunStatusRunning {
		return false, false
	}
	if l.Facts.sessionHumanApprovalAwaiting(ctx, sessionID) {
		return false, false
	}
	if l.Facts.sessionHostObligationHeld(ctx, sessionID) {
		return false, false
	}
	if scaffoldvars.HasPendingUserInput(vars) {
		return false, false
	}
	return true, false
}
func (l *Admission) ConsumeBudget(ctx context.Context, sessionID, runID string, wake anchor.ID) bool {
	if strings.TrimSpace(runID) == "" {
		run, _, ok := l.Facts.activeRunAndVars(ctx, sessionID)
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
