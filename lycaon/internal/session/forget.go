package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/resourcelifecycle"
)

// RegisterSessionCleanup adds one session-scoped cleanup rule.
func (m *Manager) RegisterSessionCleanup(name string, order int, cleanup func(context.Context, string) error) error {
	if m == nil {
		return nil
	}
	return m.ensureResourceRegistry().Register(resourcelifecycle.ScopeSession, name, order, func(ctx context.Context, scope resourcelifecycle.Scope) error {
		return cleanup(ctx, scope.ID)
	})
}

// RegisterSessionDisposal runs cleanup only when a session is disposed, not on
// Stop. Chat-lifetime authority is released here.
func (m *Manager) RegisterSessionDisposal(name string, order int, cleanup func(context.Context, string) error) error {
	if m == nil {
		return nil
	}
	return m.ensureResourceRegistry().RegisterDisposal(resourcelifecycle.ScopeSession, name, order, func(ctx context.Context, scope resourcelifecycle.Scope) error {
		return cleanup(ctx, scope.ID)
	})
}

func (m *Manager) ensureResourceRegistry() *resourcelifecycle.Registry {
	if m.resources != nil {
		return m.resources
	}
	m.resources = resourcelifecycle.New()
	_ = m.resources.Register(resourcelifecycle.ScopeSession, "session-memory", 100, func(ctx context.Context, scope resourcelifecycle.Scope) error {
		m.releaseSessionMemory(ctx, scope.ID)
		return nil
	})
	_ = m.resources.RegisterDisposal(resourcelifecycle.ScopeSession, "chat-sandbox-authority", 101, func(_ context.Context, scope resourcelifecycle.Scope) error {
		m.forgetSandboxAuthority(scope.ID)
		return nil
	})
	return m.resources
}

// DisposeSessionResources permanently closes a session resource scope.
func (m *Manager) DisposeSessionResources(ctx context.Context, sessionID string) {
	if m == nil {
		return
	}
	if m.resources == nil {
		m.releaseSessionMemory(ctx, sessionID)
		m.forgetSandboxAuthority(sessionID)
	} else {
		_ = m.resources.Dispose(ctx, resourcelifecycle.SessionScope(sessionID))
	}
	if m.queue != nil {
		m.queue.Clear(sessionID)
	}
}

func (m *Manager) releaseSessionMemory(ctx context.Context, sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	m.Runner.Execution.Cancel(sessionID)
	m.Runner.Curation.Cancel(sessionID)
	if m.Runner.History.Runner != nil {
		m.Runner.History.Runner.CancelSession(sessionID)
	}
	m.Research.CancelSession(sessionID)
	if m.coordinatorRuntime != nil {
		m.coordinatorRuntime.ForgetSession(ctx, sessionID)
	}
	if m.workflows != nil {
		m.workflows.Cleanup.ForgetSession(sessionID)
	}
	m.Captures.Capture.Reset(sessionID)
	m.Transcript.Streams.Finish(ctx, sessionID)
	m.ProgressClosure.Forget(sessionID)
	m.Runner.Closeouts.Forget(sessionID)
	m.Runner.Settlement.Forget(sessionID)
	m.Runner.Spend.Forget(ctx, sessionID)
	m.Batch.BeginTurn(sessionID)
	m.Promotion.Forget(sessionID)
	m.Runner.History.ForgetCalibration(sessionID)
	m.Gate.Forget(sessionID)
	m.PolicyIndex.Forget(sessionID)
	progress.ForgetClock(sessionID)
	m.Protection.Forget(sessionID)
	if m.toolApprovalCoalesce != nil {
		m.toolApprovalCoalesce.ForgetSession(sessionID)
	}
	if m.gateRepeatLedger != nil {
		m.gateRepeatLedger.ForgetSession(sessionID)
	}
	if m.ToolPolicy.Pipeline != nil {
		m.ToolPolicy.Pipeline.Counters().ForgetSession(sessionID)
	}
	if m.writeRootRuntime != nil {
		m.writeRootRuntime.ReleaseRun(sessionID)
	}
	if m.listenRuntime != nil {
		m.listenRuntime.ReleaseRun(sessionID)
	}
	if m.loopbackRuntime != nil {
		m.loopbackRuntime.ReleaseRun(sessionID)
	}
	if m.toolRejectFormatter != nil {
		m.toolRejectFormatter.ForgetSession(sessionID)
	}
	if m.toolOutputEnricher != nil {
		m.toolOutputEnricher.ForgetSession(sessionID)
	}
}

// forgetSandboxAuthority releases the approved sandbox grants a chat keeps
// across Stop. It runs only when the chat is disposed.
func (m *Manager) forgetSandboxAuthority(sessionID string) {
	if m.writeRootRuntime != nil {
		m.writeRootRuntime.ForgetSession(sessionID)
	}
	if m.listenRuntime != nil {
		m.listenRuntime.ForgetSession(sessionID)
	}
	if m.loopbackRuntime != nil {
		m.loopbackRuntime.ForgetSession(sessionID)
	}
}
