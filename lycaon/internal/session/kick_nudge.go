package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

// InstallAnchorRegistry installs configured anchor bindings.
func (m *Manager) InstallAnchorRegistry() error {
	if m == nil {
		return nil
	}
	reg, err := anchor.LoadRegistryFromConfigRoot()
	if err != nil {
		return fmt.Errorf("load anchor registry: %w", err)
	}
	anchor.SetDefaultRegistry(reg)
	m.ensureCoordinatorRuntime().Anchors().SetRegistry(reg)
	return nil
}

// Emit queues guidance for the next prompt.
func (m *Manager) Emit(ctx context.Context, sessionID string, id anchor.ID, env anchor.Envelope) {
	m.ensureCoordinatorRuntime().Anchors().Emit(ctx, sessionID, id, env)
}

// EmitMatch queues guidance with selector facts.
func (m *Manager) EmitMatch(ctx context.Context, sessionID string, id anchor.ID, env anchor.Envelope, match anchor.MatchContext) {
	m.ensureCoordinatorRuntime().Anchors().EmitMatch(ctx, sessionID, id, env, match)
}

// DropCoordinatorKick removes queued or staged guidance.
func (m *Manager) DropCoordinatorKick(sessionID string, id anchor.ID) {
	m.ensureCoordinatorRuntime().Anchors().Drop(sessionID, id)
}

// coordinatorKickIDs drops guidance whose live obligation has cleared and
// lists the kicks the turn opening now delivers.
func (m *Manager) coordinatorKickIDs(ctx context.Context, sessionID string) []string {
	return m.ensureCoordinatorRuntime().Kicks().PendingKickIDsUnless(sessionID, m.coordinatorKickCleared(ctx, sessionID))
}

// coordinatorKickCleared reports guidance whose obligation cleared while it waited.
func (m *Manager) coordinatorKickCleared(ctx context.Context, sessionID string) kick.KickSkip {
	feedbackPending := anchor.InformRender(anchor.FeedbackPending)
	budgetRequested := anchor.InformRender(anchor.WorkerBudgetRequested)
	return func(id, subject string) bool {
		if anchor.SameInform(id, anchor.ReviewLoopContinue) || anchor.SameInform(id, anchor.ReviewLoopDecide) {
			return m.workflows != nil && !m.workflows.ActiveReviewVerdictPending(ctx, sessionID)
		}
		if id == budgetRequested {
			return !m.workerBudgetRequestOpen(subject)
		}
		if id != feedbackPending {
			return false
		}
		pending, known := m.sessionPendingUserInputKnown(ctx, sessionID)
		if !known {
			return false
		}
		return !pending
	}
}

func (m *Manager) sessionPendingUserInputKnown(ctx context.Context, sessionID string) (pending bool, known bool) {
	if m == nil || m.workflows == nil {
		return false, false
	}
	vars, err := m.workflows.ScaffoldVarsForSession(ctx, sessionID)
	if err != nil || vars == nil {
		return false, false
	}
	return scaffoldvars.HasPendingUserInput(vars), true
}

// EmitEager queues first-turn worker guidance.
func (m *Manager) EmitEager(ctx context.Context, sessionID string, id anchor.ID, data map[string]string) {
	m.ensureCoordinatorRuntime().Anchors().EmitEager(ctx, sessionID, id, data)
}

// deliverPendingKicks records every kick queued for the turn, oldest first,
// so a turn opened for one fact also sees the facts that waited before it.
func (m *Manager) deliverPendingKicks(ctx context.Context, sessionID string) error {
	kicks := m.ensureCoordinatorRuntime().Kicks()
	cleared := m.coordinatorKickCleared(ctx, sessionID)
	renderCtx := m.coordinatorKickRenderContext(ctx, sessionID)
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

// An empty return means no user message was appended.
func (m *Manager) applyPromptUserTurn(ctx context.Context, id string, in PromptInput) (string, error) {
	visibleContent := strings.TrimSpace(in.Text)
	userPrompt := promptUserInstruction(in)
	if err := m.deliverPendingKicks(ctx, id); err != nil {
		return "", err
	}
	if visibleContent == "" && len(in.ArtifactIDs) == 0 {
		return "", nil
	}
	if in.HostSignal == nil {
		var err error
		if in.AuthorPersonID, err = m.promptAuthor(ctx, in); err != nil {
			return "", err
		}
	}
	if in.Continuation {
		_, err := m.appendUserContinuation(ctx, id, in)
		return "", err
	}
	userMsg := api.Message{
		ID:        promptMessageID(in),
		Role:      api.MessageRoleUser,
		Content:   visibleContent,
		Origin:    api.MessageOriginUser,
		Authority: api.ContentAuthorityUser, TrustTier: api.ContentTrustTierTrusted,
		AuthorPersonID: in.AuthorPersonID,
		ContentParts:   append([]api.MessageContentPart(nil), in.ContentParts...),
		SourceContext:  in.SourceContext,
		ArtifactIDs:    append([]string(nil), in.ArtifactIDs...),
		CreatedAt:      time.Now().UTC(),
	}
	if in.HostSignal != nil {
		userMsg.Origin = api.MessageOriginHost
		userMsg.Authority = api.ContentAuthoritySystem
		userMsg.Kind = in.HostSignal.Kind
		userMsg.HostSignalID = in.HostSignal.ID
	}
	stampUserMessageVisibility(&userMsg)
	if userPrompt != "" {
		m.maybeBootstrapProgress(ctx, id, userMsg)
	}
	m.createUserTurnReviewCheckpoint(ctx, id, userMsg)
	// The rewind anchor precedes the user message.
	if err := m.sealPromptCheckpoint(ctx, id, userMsg.ID); err != nil {
		return "", err
	}
	if err := m.appendMessages(ctx, id, userMsg); err != nil {
		// The admission ID makes a replayed append idempotent.
		if strings.TrimSpace(in.SubmissionID) == "" || !errors.Is(err, store.ErrDuplicateMessageID) {
			return "", err
		}
	}
	if userPrompt != "" {
		m.maybeResetCoordinatorBatchOnVisibleUser(ctx, id, userMsg)
		// Only visible user intent resolves pending feedback.
		if m.workflows != nil && isVisibleUserIntentMessage(userMsg) {
			if err := m.workflows.TryResolveUserFeedback(ctx, id, userMsg.ID, userMsg.AuthorPersonID, userPrompt); err != nil {
				return "", fmt.Errorf("resolve user feedback: %w", err)
			}
		}
		if m.toolApprovalCoalesce != nil && isVisibleUserIntentMessage(userMsg) {
			m.toolApprovalCoalesce.NoteUserIntentBoundary(id)
		}
		if m.gateRepeatLedger != nil && isVisibleUserIntentMessage(userMsg) {
			// User intent resets per-turn repeat counts.
			m.gateRepeatLedger.NoteUserIntentBoundary(id)
		}
		if m.writeRootRuntime != nil && isVisibleUserIntentMessage(userMsg) {
			// Parent intent clears write denials for its session tree.
			m.writeRootRuntime.NoteUserIntentBoundary(id)
		}
		if m.listenRuntime != nil && isVisibleUserIntentMessage(userMsg) {
			m.listenRuntime.NoteUserIntentBoundary(id)
		}
		if m.loopbackRuntime != nil && isVisibleUserIntentMessage(userMsg) {
			m.loopbackRuntime.NoteUserIntentBoundary(id)
		}
	}
	return userMsg.ID, nil
}

// Admission receipts retain the original sender.
func (m *Manager) promptAuthor(ctx context.Context, in PromptInput) (string, error) {
	if in.AuthorPersonID != "" {
		return in.AuthorPersonID, nil
	}
	author, err := people.Acting(ctx, m.store)
	if err != nil {
		return "", fmt.Errorf("resolve prompt author: %w", err)
	}
	return author.ID, nil
}

func promptMessageID(in PromptInput) string {
	if id := strings.TrimSpace(in.SubmissionID); id != "" {
		return id
	}
	if len(in.SubmissionIDs) > 0 {
		if id := strings.TrimSpace(in.SubmissionIDs[0]); id != "" {
			return id
		}
	}
	return uuid.NewString()
}

// appendUserContinuation records direction inside the open visible turn.
func (m *Manager) appendUserContinuation(ctx context.Context, sessionID string, in PromptInput) (api.Message, error) {
	msg := api.Message{
		ID: promptMessageID(in), Role: api.MessageRoleUser,
		ContentParts:  append([]api.MessageContentPart(nil), in.ContentParts...),
		SourceContext: in.SourceContext,
		ArtifactIDs:   append([]string(nil), in.ArtifactIDs...),
		Content:       strings.TrimSpace(in.Text), Kind: api.MessageKindUserContinuation,
		Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser,
		AuthorPersonID: in.AuthorPersonID,
		TrustTier:      api.ContentTrustTierTrusted, CreatedAt: time.Now().UTC(),
	}
	stampUserMessageVisibility(&msg)
	if err := m.appendMessages(ctx, sessionID, msg); err != nil && !errors.Is(err, store.ErrDuplicateMessageID) {
		return api.Message{}, err
	}
	if m.workflows != nil && in.Recovery == nil && api.IsUserInstructionMessage(msg) {
		if err := m.workflows.TryResolveUserFeedback(ctx, sessionID, msg.ID, msg.AuthorPersonID, msg.Content); err != nil {
			return api.Message{}, fmt.Errorf("resolve user feedback from continuation: %w", err)
		}
	}
	return msg, nil
}

func (m *Manager) appendHostKickMessage(ctx context.Context, sessionID, kickID, nudge string) (api.Message, error) {
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
	return kickMsg, m.appendMessages(ctx, sessionID, kickMsg)
}

// takePhaseGuidance renders and records the guidance of a phase entered since
// the coordinator's last model call. Only a coordinator turn waits on a phase.
func (m *Manager) takePhaseGuidance(ctx context.Context, sessionID string) ([]api.Message, error) {
	kicks := m.ensureCoordinatorRuntime().Kicks()
	key := anchor.PhaseEntered.String()
	if !kicks.HasLatest(sessionID, key) {
		return nil, nil
	}
	kickID, text, lease, ok, err := kicks.RenderLatest(ctx, sessionID, key, m.coordinatorKickRenderContext(ctx, sessionID))
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

func (m *Manager) coordinatorKickRenderContext(ctx context.Context, sessionID string) kick.CoordinatorKickRenderContext {
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
	state := m.BuildImplementSessionState(ctx, sess)
	rc.BatchPhase = state.BatchPhase
	rc.BatchSeq = state.BatchSeq
	rc.PendingOverlayJobs = append([]string(nil), state.PendingOverlayIDs...)
	msgs, msgErr := m.store.GetMessages(ctx, sessionID)
	if msgErr == nil {
		rc.PartialWorkerJobs = surface.PartialWorkerSummaryJobIDs(msgs)
	}
	if isCoordinatorParentSession(sess) {
		paths := ParentSessionMergedWriteChangedPaths(ctx, m.workerQueue, sess.ProjectID, sess.ID)
		if len(paths) > 0 {
			rc.PromotedPaths = paths
		}
	}
	frame := inject.CoordinatorTurnFrame{}
	if m.coordinatorFrame != nil {
		if built, err := m.coordinatorFrame.BuildCoordinatorTurnFrame(ctx, sessionID, sess); err == nil {
			frame = built
			rc.WorkflowID = strings.TrimSpace(frame.RunContext.WorkflowID)
			rc.CurrentPhase = strings.TrimSpace(frame.RunContext.CurrentPhase)
			rc.AdvanceWhenGateMet = frame.RunContext.AdvanceWhenGateMet
			rc.FailedLeaves = append([]string(nil), frame.RunContext.FailedLeaves...)
		}
	}
	rc.GateObligations = m.projectKickGateObligations(ctx, frame)
	rc.ProgressOpenItems, rc.ProgressClosureArmed = m.kickProgressClosureState(ctx, sessionID)
	return rc
}

// A disarmed closure latch needs no progress count.
func (m *Manager) kickProgressClosureState(ctx context.Context, sessionID string) (open int, armed bool) {
	if m == nil || m.progress == nil {
		return 0, false
	}
	root := RootSessionID(ctx, m.store, sessionID)
	if _, armed = m.progressClosureLatch(root); !armed {
		return 0, false
	}
	_, pending, _ := progress.CloseCounts(m.progress.Get(ctx, root))
	return pending, true
}

// Only the active phase contributes open workflow obligations.
func (m *Manager) workflowObligationsOpen(ctx context.Context, sessionID string) bool {
	if m == nil || m.workflows == nil || m.coordinatorFrame == nil {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	run, err := m.workflows.GetActive(ctx, sessionID)
	if err != nil || run == nil || run.Status != api.WorkflowRunStatusRunning {
		return false
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return false
	}
	frame, err := m.coordinatorFrame.BuildCoordinatorTurnFrame(ctx, sessionID, sess)
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

func (m *Manager) projectKickGateObligations(ctx context.Context, frame inject.CoordinatorTurnFrame) []kick.GateObligation {
	if m == nil || m.gateFeedback == nil {
		return nil
	}
	runCtx := frame.RunContext
	leaves := unsatisfiedFrameGateLeaves(frame)
	extras := feedback.PlanStubGateExtras(frame.Runtime.BlueprintBody)
	if frame.Runtime.PhaseExit != nil {
		extras = feedback.WithReviewAgents(extras, frame.Runtime.PhaseExit.ReviewAgents)
	}
	rows := m.gateFeedback.ProjectObligations(ctx, leaves, runCtx.AdvanceWhenGateMet, extras)
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
