package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/resourcelifecycle"
	"log/slog"
	"strings"
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
	m.CancelInFlightPrompt(sessionID)
	m.curation.Cancel(sessionID)
	if m.compactionRunner != nil {
		m.compactionRunner.CancelSession(sessionID)
	}
	if m.indexWarmer != nil {
		m.indexWarmer.CancelSession(sessionID)
	}
	if m.coordinatorRuntime != nil {
		m.coordinatorRuntime.ForgetSession(ctx, sessionID)
	}
	if m.workflows != nil {
		m.workflows.Cleanup.ForgetSession(sessionID)
	}
	m.checkpointCapture.Reset(sessionID)
	m.Streams().Finish(ctx, sessionID)
	m.progressClosureExpect.Delete(sessionID)
	m.closeout.forget(sessionID)
	m.deferredTurnSettlement.remove(sessionID)
	if m.cost != nil {
		if err := m.cost.ClearSpendWarning(ctx, sessionID); err != nil {
			slog.WarnContext(ctx, "clear spend warning", "session_id", sessionID, "error", err)
		}
	}
	m.coordinatorBatchTurn.Delete(sessionID)
	m.mergeReconcile.Delete(sessionID)
	m.compactionTokenCalibration.Delete(sessionID)
	m.promotePathStatus.Delete(sessionID)
	m.stopState.Forget(sessionID)
	m.agentsMDCache.Delete(sessionID)
	progress.ForgetClock(sessionID)
	m.clearReconstructedDirectIP(sessionID)
	if m.toolApprovalCoalesce != nil {
		m.toolApprovalCoalesce.ForgetSession(sessionID)
	}
	if m.gateRepeatLedger != nil {
		m.gateRepeatLedger.ForgetSession(sessionID)
	}
	if m.oarPipeline != nil {
		m.oarPipeline.Counters().ForgetSession(sessionID)
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

// ForgetJob releases terminal worker wake payloads.
func (m *Manager) ForgetJob(jobID string) {
	if m == nil {
		return
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return
	}
	m.workerDigests.Delete(jobID)
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
