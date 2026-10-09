package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type SessionFactsDeps struct {
	GetSession              func(ctx context.Context, sessionID string) (*api.Session, error)
	Limits                  func(context.Context, *api.Session) settings.SessionLimits
	WorkflowObligationsOpen func(ctx context.Context, sessionID string) bool
	WorkflowSource          *WorkflowDomains
}
type SessionFacts struct {
	depsMu sync.RWMutex
	deps   SessionFactsDeps
}

func (l *SessionFacts) setDeps(deps SessionFactsDeps) {
	l.depsMu.Lock()
	l.deps = deps
	l.depsMu.Unlock()
}
func (l *SessionFacts) loopDeps() SessionFactsDeps {
	if l == nil {
		return SessionFactsDeps{}
	}
	l.depsMu.RLock()
	defer l.depsMu.RUnlock()
	return l.deps
}
func (l *SessionFacts) workflowObligationsOpen(ctx context.Context, sessionID string) bool {
	if l == nil {
		return false
	}
	deps := l.loopDeps()
	if deps.WorkflowObligationsOpen == nil {
		return false
	}
	return deps.WorkflowObligationsOpen(ctx, sessionID)
}
func (l *SessionFacts) sessionHumanApprovalAwaiting(ctx context.Context, sessionID string) bool {
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
func (l *SessionFacts) sessionHostObligationHeld(ctx context.Context, sessionID string) bool {
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
func (l *SessionFacts) hostObligationParkReason(ctx context.Context, sessionID string) string {
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
func (l *SessionFacts) capSleepDuration(ctx context.Context, sessionID string, requested time.Duration) time.Duration {
	return CapWaitDuration(requested, l.sessionLimits(ctx, sessionID).CoordinatorMaxSleep())
}
func (l *SessionFacts) sessionLimits(ctx context.Context, sessionID string) settings.SessionLimits {
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
func (l *SessionFacts) activeRunAndVars(ctx context.Context, sessionID string) (*api.WorkflowRun, map[string]any, bool) {
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
func (l *SessionFacts) activeRunID(ctx context.Context, sessionID string) string {
	run, _, ok := l.activeRunAndVars(ctx, sessionID)
	if !ok || run == nil {
		return ""
	}
	return run.ID
}
func (l *SessionFacts) sessionHasPendingUserInput(ctx context.Context, sessionID string) bool {
	_, vars, ok := l.activeRunAndVars(ctx, sessionID)
	if !ok || vars == nil {
		return false
	}
	return scaffoldvars.HasPendingUserInput(vars)
}
func (l *SessionFacts) pendingUserInputWaitDeadline(ctx context.Context, sessionID string) time.Time {
	return time.Now().UTC().Add(l.sessionLimits(ctx, sessionID).CoordinatorMaxSleep())
}
