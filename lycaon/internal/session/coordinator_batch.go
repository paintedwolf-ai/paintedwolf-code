package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

func (m *Manager) applyCoordinatorBatchEvent(ctx context.Context, sessionID string, ev batch.Event, eventSeq int) {
	if m == nil || m.workflows == nil {
		return
	}
	_ = m.workflows.Batch.ApplyCoordinatorBatchEvent(ctx, sessionID, ev, eventSeq)
}

func (m *Manager) coordinatorBatchSeqFromState(state surface.ImplementSessionState) int {
	return state.BatchSeq
}

func (m *Manager) maybeResetCoordinatorBatchOnVisibleUser(ctx context.Context, sessionID string, msg api.Message) {
	if m == nil || !isVisibleUserIntentMessage(msg) {
		return
	}
	m.applyCoordinatorBatchEvent(ctx, sessionID, batch.EventVisibleUserMessage, 0)
}

func isVisibleUserIntentMessage(msg api.Message) bool {
	return api.IsUserIntentMessage(msg) &&
		msg.Origin == api.MessageOriginUser &&
		strings.TrimSpace(msg.Content) != ""
}

func (m *Manager) maybeAdvanceCoordinatorBatchOnTaskEnqueued(ctx context.Context, sessionID, agentType string) {
	if m == nil || m.store == nil {
		return
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil || sess.IsWorkerChild() {
		return
	}
	state := m.BuildImplementSessionState(ctx, sess)
	seq := m.coordinatorBatchSeqFromState(state)
	agentType = strings.TrimSpace(strings.ToLower(agentType))
	if capable, known := prompts.AgentMutationCapable(agentType); known && capable {
		m.applyCoordinatorBatchEvent(ctx, sessionID, batch.EventWriterTaskEnqueued, seq)
	}
}

// ReconcileCoordinatorBatchFromLedgerForTest drives batch reconcile for unit tests.
func (m *Manager) ReconcileCoordinatorBatchFromLedgerForTest(ctx context.Context, sessionID string) {
	m.reconcileCoordinatorBatchFromLedger(ctx, sessionID)
}

func (m *Manager) MaybeAdvanceCoordinatorBatchOnTaskEnqueuedForTest(ctx context.Context, sessionID, agentType string) {
	m.maybeAdvanceCoordinatorBatchOnTaskEnqueued(ctx, sessionID, agentType)
}

// reconcileCoordinatorBatchFromLedger is the single host path for batch phase and
// overlay-integrate completion derived from the worker job ledger.
func (m *Manager) reconcileCoordinatorBatchFromLedger(ctx context.Context, sessionID string) {
	if m == nil || m.store == nil {
		return
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil || sess.IsWorkerChild() {
		return
	}
	state := m.BuildImplementSessionState(ctx, sess)
	seq := m.coordinatorBatchSeqFromState(state)

	if len(state.PendingOverlayIDs) > 0 {
		idle, idleErr := ParentSessionWorkerCycleIdle(ctx, m.workerQueue, sess.ProjectID, sessionID, "")
		if idleErr == nil && idle {
			m.applyCoordinatorBatchEvent(ctx, sessionID, batch.EventOverlaysPendingIdle, seq)
		}
		return
	}

	idle, err := ParentSessionWorkerCycleIdle(ctx, m.workerQueue, sess.ProjectID, sessionID, "")
	if err != nil || !idle {
		return
	}
	msgs, msgErr := m.store.GetMessages(ctx, sessionID)
	if msgErr != nil {
		return
	}
	if len(surface.PartialWorkerSummaryJobIDs(msgs)) > 0 {
		return
	}
	if state.WorkersInFlight > 0 {
		return
	}
	progressContent := ""
	if m.progress != nil {
		progressContent = m.progress.Get(ctx, RootSessionID(ctx, m.store, sess.ID))
	}
	verifyRequired, verifyPassed, _, verifyUnverified := m.workflowVerifyGateState(ctx, sess, msgs)
	if !BatchReadyForSynthesis(state, msgs, progressContent, verifyRequired, verifyPassed || verifyUnverified) {
		return
	}

	if state.BatchPhase == batch.PhaseIntegrate {
		m.queueOverlayIntegrateCompleteKick(ctx, sess, sessionID)
		m.applyCoordinatorBatchEvent(ctx, sessionID, batch.EventSynthesisReady, seq)
		return
	}
	if surface.TerminalWorkerCompletionSince(msgs, api.UserIntentBoundary(msgs)) {
		m.applyCoordinatorBatchEvent(ctx, sessionID, batch.EventSynthesisReady, seq)
	}
}

func (m *Manager) queueOverlayIntegrateCompleteKick(ctx context.Context, sess *api.Session, sessionID string) {
	if m == nil || sess == nil {
		return
	}
	paths := ParentSessionMergedWriteChangedPaths(ctx, m.workerQueue, sess.ProjectID, sess.ID)
	env := anchor.Envelope{}
	if len(paths) > 0 {
		env.PromotedPaths = paths
	}
	m.Emit(ctx, sessionID, anchor.OverlayPromoteComplete, env)
}

func (m *Manager) disarmCoordinatorLoopIfBatchTerminal(ctx context.Context, sessionID string) {
	if m == nil || m.store == nil {
		return
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil || sess.IsWorkerChild() {
		return
	}
	state := m.BuildImplementSessionState(ctx, sess)
	switch state.BatchPhase {
	case batch.PhaseSynthesize, batch.PhaseClosed:
		m.ensureCoordinatorRuntime().CoordinatorLoop().DisarmTimerBackstop(ctx, sessionID)
	}
}
