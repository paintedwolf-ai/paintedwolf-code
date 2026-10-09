package guidancedelivery

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/pkg/api"
)

// InstallAnchorRegistry installs configured anchor bindings.
func (m *Service) InstallAnchorRegistry() error {
	if m == nil {
		return nil
	}
	reg, err := anchor.LoadRegistryFromConfigRoot()
	if err != nil {
		return fmt.Errorf("load anchor registry: %w", err)
	}
	anchor.SetDefaultRegistry(reg)
	m.anchors.SetRegistry(reg)
	return nil
}

// Emit queues guidance for the next prompt.
func (m *Service) Emit(ctx context.Context, sessionID string, id anchor.ID, env anchor.Envelope) {
	m.anchors.Emit(ctx, sessionID, id, env)
}

// EmitMatch queues guidance with selector facts.
func (m *Service) EmitMatch(ctx context.Context, sessionID string, id anchor.ID, env anchor.Envelope, match anchor.MatchContext) {
	m.anchors.EmitMatch(ctx, sessionID, id, env, match)
}

// DropCoordinatorKick removes queued or staged guidance.
func (m *Service) Drop(sessionID string, id anchor.ID) {
	m.anchors.Drop(sessionID, id)
}

// coordinatorKickIDs drops guidance whose live obligation has cleared and
// lists the kicks the turn opening now delivers.
func (m *Service) PendingIDs(ctx context.Context, sessionID string) []string {
	return m.kicks.PendingKickIDsUnless(sessionID, m.cleared(ctx, sessionID))
}

// coordinatorKickCleared reports guidance whose obligation cleared while it waited.
func (m *Service) cleared(ctx context.Context, sessionID string) kick.KickSkip {
	feedbackPending := anchor.InformRender(anchor.FeedbackPending)
	budgetRequested := anchor.InformRender(anchor.WorkerBudgetRequested)
	return func(id, subject string) bool {
		if anchor.SameInform(id, anchor.ReviewLoopContinue) || anchor.SameInform(id, anchor.ReviewLoopDecide) {
			return m.workflows != nil && !m.workflows.Policy.ActiveReviewVerdictPending(ctx, sessionID)
		}
		if id == budgetRequested {
			return !m.budgets.BudgetRequestOpen(subject)
		}
		if id != feedbackPending {
			return false
		}
		pending, known := m.pendingInput(ctx, sessionID)
		if !known {
			return false
		}
		return !pending
	}
}

func (m *Service) pendingInput(ctx context.Context, sessionID string) (pending bool, known bool) {
	if m == nil || m.workflows == nil {
		return false, false
	}
	vars, err := m.workflows.Policy.ScaffoldVarsForSession(ctx, sessionID)
	if err != nil || vars == nil {
		return false, false
	}
	return scaffoldvars.HasPendingUserInput(vars), true
}

// EmitEager queues first-turn worker guidance.
func (m *Service) EmitEager(ctx context.Context, sessionID string, id anchor.ID, data map[string]string) {
	m.anchors.EmitEager(ctx, sessionID, id, data)
}

// deliverPendingKicks records every kick queued for the turn, oldest first,
// so a turn opened for one fact also sees the facts that waited before it.
func (m *Service) DeliverPending(ctx context.Context, sessionID string) error {
	kicks := m.kicks
	cleared := m.cleared(ctx, sessionID)
	renderCtx := m.renderContext(ctx, sessionID)
	for range kick.MaxQueuedGuidance + 1 {
		kickID := kicks.TakePendingKickIDUnless(sessionID, cleared)
		if kickID == "" {
			return nil
		}
		nudge, lease, ok, err := kicks.RenderPendingNudge(ctx, sessionID, renderCtx)
		if err != nil {
			return fmt.Errorf("render pending coordinator kick: %w", err)
		}
		if !ok {
			continue
		}
		if _, err := m.appendHostKickMessage(ctx, sessionID, kickID, nudge); err != nil {
			return err
		}
		kicks.AckPendingNudge(sessionID, lease)
	}
	return nil
}

func (m *Service) appendHostKickMessage(ctx context.Context, sessionID, kickID, nudge string) (api.Message, error) {
	nudge = strings.TrimSpace(nudge)
	if nudge == "" {
		return api.Message{}, nil
	}
	kickID = strings.TrimSpace(kickID)
	kind := api.MessageKindHostNudge
	if anchorID, ok := anchor.ParseID(kickID); ok {
		kickID = anchorID.String()
		kind = api.MessageKindHostKick
	} else if kickID != "" {
		kind = api.MessageKindCoordinatorGuidance
	}
	kickMsg := api.Message{
		ID:        uuid.NewString(),
		Role:      api.MessageRoleUser,
		Content:   nudge,
		Origin:    api.MessageOriginHost,
		Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted,
		Kind:         kind,
		HostSignalID: kickID,
		Visibility:   api.MessageVisibilityInternal,
		CreatedAt:    time.Now().UTC(),
	}
	return kickMsg, m.transcript.Append(ctx, sessionID, kickMsg)
}

// takePhaseGuidance renders and records the guidance of a phase entered since
// the coordinator's last model call. Only a coordinator turn waits on a phase.
func (m *Service) TakePhase(ctx context.Context, sessionID string) ([]api.Message, error) {
	kicks := m.kicks
	key := anchor.PhaseEntered.String()
	if !kicks.HasLatest(sessionID, key) {
		return nil, nil
	}
	kickID, text, lease, ok, err := kicks.RenderLatest(ctx, sessionID, key, m.renderContext(ctx, sessionID))
	if err != nil {
		return nil, fmt.Errorf("render phase guidance: %w", err)
	}
	if !ok {
		return nil, nil
	}
	msg, err := m.appendHostKickMessage(ctx, sessionID, kickID, text)
	if err != nil {
		return nil, err
	}
	kicks.AckLatest(sessionID, key, lease)
	return []api.Message{msg}, nil
}

func (m *Service) renderContext(ctx context.Context, sessionID string) kick.CoordinatorKickRenderContext {
	var rc kick.CoordinatorKickRenderContext
	if m == nil || m.store == nil {
		return rc
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return rc
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return rc
	}
	state := m.state.ForSession(ctx, sess)
	rc.BatchPhase = state.BatchPhase
	rc.BatchSeq = state.BatchSeq
	rc.PendingOverlayJobs = append([]string(nil), state.PendingOverlayIDs...)
	msgs, msgErr := m.store.GetMessages(ctx, sessionID)
	if msgErr == nil {
		rc.PartialWorkerJobs = surface.PartialWorkerSummaryJobIDs(msgs)
	}
	if sess != nil && !sess.IsWorkerChild() {
		paths := workeroutcomes.ParentSessionMergedWriteChangedPaths(ctx, m.workers, sess.ProjectID, sess.ID)
		if len(paths) > 0 {
			rc.PromotedPaths = paths
		}
	}
	frame := inject.CoordinatorTurnFrame{}
	if m.frame != nil {
		if built, err := m.frame.BuildCoordinatorTurnFrame(ctx, sessionID, sess); err == nil {
			frame = built
			rc.WorkflowID = strings.TrimSpace(frame.RunContext.WorkflowID)
			rc.CurrentPhase = strings.TrimSpace(frame.RunContext.CurrentPhase)
			rc.AdvanceWhenGateMet = frame.RunContext.AdvanceWhenGateMet
			rc.FailedLeaves = append([]string(nil), frame.RunContext.FailedLeaves...)
		}
	}
	rc.GateObligations = m.GateObligations(ctx, sessionID, frame)
	rc.ProgressOpenItems, rc.ProgressClosureArmed = m.closureState(ctx, sessionID)
	return rc
}

// A disarmed closure latch needs no progress count.
func (m *Service) closureState(ctx context.Context, sessionID string) (open int, armed bool) {
	if m == nil || m.progress == nil {
		return 0, false
	}
	root := sessiontree.RootID(ctx, m.store, sessionID)
	if _, armed = m.closure.Baseline(root); !armed {
		return 0, false
	}
	_, pending, _ := progress.CloseCounts(m.progress.Get(ctx, root))
	return pending, true
}

// Only the active phase contributes open workflow obligations.
func (m *Service) WorkflowObligationsOpen(ctx context.Context, sessionID string) bool {
	if m == nil || m.workflows == nil || m.frame == nil {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	run, err := m.workflows.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || run == nil || run.Status != api.WorkflowRunStatusRunning {
		return false
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return false
	}
	frame, err := m.frame.BuildCoordinatorTurnFrame(ctx, sessionID, sess)
	if err != nil {
		return false
	}
	return len(unsatisfiedFrameGateLeaves(frame)) > 0
}

// Persisted failed leaves cover phases without runtime gate rows.
func unsatisfiedFrameGateLeaves(frame inject.CoordinatorTurnFrame) []string {
	leaves := append([]string(nil), frame.RunContext.FailedLeaves...)
	current := strings.TrimSpace(frame.RunContext.CurrentPhase)
	for _, row := range frame.Runtime.Phases {
		if row.ID != current {
			continue
		}
		leaves = inject.UnsatisfiedGateIDs(row.Gates)
		break
	}
	return leaves
}

// GateObligations projects unmet gate obligations for the coordinator turn frame.
func (m *Service) GateObligations(ctx context.Context, sessionID string, frame inject.CoordinatorTurnFrame) []kick.GateObligation {
	if m == nil || m.gateFeedback == nil {
		return nil
	}
	fb := m.gateFeedback
	if m.workflows != nil && m.workflows.Policy != nil {
		if manifest, ok := m.workflows.Policy.ActiveManifest(ctx, sessionID); ok {
			fb = fb.WithWorkflowArchive(manifest.Archive)
		}
	}
	runCtx := frame.RunContext
	leaves := unsatisfiedFrameGateLeaves(frame)
	extras := feedback.PlanStubGateExtras(frame.Runtime.BlueprintBody)
	if frame.Runtime.PhaseExit != nil {
		extras = feedback.WithReviewAgents(extras, frame.Runtime.PhaseExit.ReviewAgents)
	}
	rows := fb.ProjectObligations(ctx, leaves, runCtx.AdvanceWhenGateMet, extras)
	if len(rows) == 0 {
		return nil
	}
	out := make([]kick.GateObligation, len(rows))
	for i, row := range rows {
		out[i] = kick.GateObligation{
			ID:       row.ID,
			Purpose:  row.Purpose,
			Satisfy:  append([]string(nil), row.Satisfy...),
			Missing:  append([]string(nil), row.Missing...),
			Required: append([]string(nil), row.Required...),
		}
	}
	return out
}
