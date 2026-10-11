package batchcontrol

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) Apply(ctx context.Context, sessionID string, ev batch.Event, eventSeq int) {
	if m == nil || m.workflows == nil {
		return
	}
	_ = m.workflows.ApplyCoordinatorBatchEvent(ctx, sessionID, ev, eventSeq)
}

func (m *Service) Sequence(state surface.ImplementSessionState) int {
	return state.BatchSeq
}

func (m *Service) VisibleUser(ctx context.Context, sessionID string, msg api.Message) {
	if m == nil || !promptinput.VisibleIntent(msg) {
		return
	}
	m.Apply(ctx, sessionID, batch.EventVisibleUserMessage, 0)
}

func (m *Service) TaskEnqueued(ctx context.Context, sessionID, agentType string) {
	if m == nil || m.store == nil {
		return
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil || sess.IsWorkerChild() {
		return
	}
	state := m.state.ForSession(ctx, sess)
	seq := m.Sequence(state)
	agentType = strings.TrimSpace(strings.ToLower(agentType))
	if capable, known := prompts.AgentMutationCapable(agentType); known && capable {
		m.Apply(ctx, sessionID, batch.EventWriterTaskEnqueued, seq)
	}
}

// ReconcileCoordinatorBatchFromLedgerForTest drives batch reconcile for unit tests.

// Reconcile is the single host path for batch phase and
// overlay-integrate completion derived from the worker job ledger.
func (m *Service) Reconcile(ctx context.Context, sessionID string) {
	if m == nil || m.store == nil {
		return
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil || sess.IsWorkerChild() {
		return
	}
	state := m.state.ForSession(ctx, sess)
	seq := m.Sequence(state)

	if len(state.PendingOverlayIDs) > 0 {
		idle, idleErr := workeroutcomes.ParentSessionWorkerCycleIdle(ctx, m.workerQueue, sess.ProjectID, sessionID, "")
		if idleErr == nil && idle {
			m.Apply(ctx, sessionID, batch.EventOverlaysPendingIdle, seq)
		}
		return
	}

	idle, err := workeroutcomes.ParentSessionWorkerCycleIdle(ctx, m.workerQueue, sess.ProjectID, sessionID, "")
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
		progressContent = m.progress.Get(ctx, sessiontree.RootID(ctx, m.store, sess.ID))
	}
	verifyRequired, verifyPassed, _, verifyUnverified := m.verification.WorkflowGateState(ctx, sess, msgs)
	if !workeroutcomes.BatchReadyForSynthesis(state, msgs, progressContent, verifyRequired, verifyPassed || verifyUnverified) {
		return
	}

	if state.BatchPhase == batch.PhaseIntegrate {
		m.IntegrateComplete(ctx, sess, sessionID)
		m.Apply(ctx, sessionID, batch.EventSynthesisReady, seq)
		return
	}
	if surface.TerminalWorkerCompletionSince(msgs, api.UserIntentBoundary(msgs)) {
		m.Apply(ctx, sessionID, batch.EventSynthesisReady, seq)
	}
}

func (m *Service) IntegrateComplete(ctx context.Context, sess *api.Session, sessionID string) {
	if m == nil || sess == nil {
		return
	}
	paths := workeroutcomes.ParentSessionMergedWriteChangedPaths(ctx, m.workerQueue, sess.ProjectID, sess.ID)
	env := anchor.Envelope{}
	if len(paths) > 0 {
		env.PromotedPaths = paths
	}
	m.guidance.Emit(ctx, sessionID, anchor.OverlayPromoteComplete, env)
}

func (m *Service) DisarmTerminal(ctx context.Context, sessionID string) {
	if m == nil || m.store == nil {
		return
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil || sess.IsWorkerChild() {
		return
	}
	state := m.state.ForSession(ctx, sess)
	switch state.BatchPhase {
	case batch.PhaseSynthesize, batch.PhaseClosed:
		m.loop.DisarmTimerBackstop(ctx, sessionID)
	}
}
