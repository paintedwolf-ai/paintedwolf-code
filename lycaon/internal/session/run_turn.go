package session

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// RunTurnBusyWindowFaultForTest injects a panic after the busy transition.
var RunTurnBusyWindowFaultForTest func()

func (m *Manager) runTurnLocked(ctx context.Context, id string, in PromptInput) (resp *promptresult.Result, runErr error) {
	ctx, endCancelScope, err := m.beginTurnCancelScope(ctx, id)
	if err != nil {
		return nil, err
	}
	defer endCancelScope()

	sess, err := m.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	releaseRuntime, err := m.beginProjectRuntime(sess.ProjectID)
	if err != nil {
		return nil, err
	}
	defer releaseRuntime()
	if m.llmSvc != nil && m.llmSvc.Policy != nil {
		policy, policyErr := m.llmSvc.Policy.GetForProjectRoots(m.overlayRootPaths(ctx, sess))
		if policyErr != nil {
			return nil, policyErr
		}
		ctx = llm.WithThinkingPolicy(ctx, policy)
	}

	if m.grounding != nil && m.grounding.IsEscalated(id) {
		return nil, ErrGroundingEscalated
	}

	if err := m.checkSpendCeiling(ctx, id, sess); err != nil {
		if errors.Is(err, ErrSessionSpendCeiling) {
			return nil, errors.Join(err, m.preserveBlockedUserPrompt(context.WithoutCancel(ctx), id, in))
		}
		slog.WarnContext(ctx, "check spend ceiling", "session_id", id, "error", err)
	}

	text := promptUserInstruction(in)
	hostTurn := in.HostSignal != nil
	if !hostTurn && in.Recovery == nil && m.workflows != nil {
		if resp, handled, err := m.workflows.Slash.TrySlashPrompt(ctx, id, text, in.SubmissionID); handled {
			// A parked phase cancels the current turn.
			return resp, mapPromptRunError(err)
		}
		prepared, workflowResp, handled, err := m.workflows.Requests.PrepareUserRequest(ctx, id, text)
		if err != nil {
			return nil, mapPromptRunError(err)
		}
		if handled {
			return workflowResp, nil
		}
		// Workflow attachment can change the session posture.
		sess, err = m.store.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if prepared != text {
			in.Text = prepared
			text = prepared
		}
	}
	if err := m.assertWorkflowRunnable(ctx, id); err != nil {
		return nil, err
	}
	turnExecution, err := m.beginDurableTurn(ctx, sess, in)
	if err != nil {
		return nil, err
	}
	defer m.claimTurnLiveness(sess.ProjectID, turnExecution.Turn.ID)()
	finishPromptExecution := m.ensureCoordinatorRuntime().CoordinatorLoop().Admission.BeginPromptExecution(ctx, id)
	defer finishPromptExecution()
	ctx = workercontext.WithJob(ctx, in.WorkerJobID)
	finalOutputID, closeoutID := "", ""
	defer func() {
		if err := m.finishDurableTurn(context.WithoutCancel(ctx), turnExecution, finalOutputID, resp, runErr); err != nil {
			resp = nil
			runErr = errors.Join(runErr, err)
		}
	}()

	if err := m.maybeUnarchiveOnPrompt(ctx, sess); err != nil {
		return nil, err
	}
	turnWasIdle := sess.Status != api.SessionStatusBusy
	if turnWasIdle {
		if err := m.store.SetSessionStatus(ctx, id, api.SessionStatusBusy); err != nil {
			return nil, err
		}
	}
	m.markSubmissionTurnBegan(ctx)
	finishPromptRegistered := false
	defer func() {
		if finishPromptRegistered {
			return
		}
		if err := m.settleUserTurn(context.WithoutCancel(ctx), id, m.turnEndDisposition(true)); err != nil {
			resp = nil
			runErr = errors.Join(runErr, fmt.Errorf("mark panicked prompt session idle: %w", err))
		}
	}()
	defer func() {
		if r := recover(); r != nil {
			slog.ErrorContext(ctx, "session turn panicked", "session_id", id,
				"panic", fmt.Sprintf("%v", r), "stack", string(debug.Stack()))
			resp = nil
			runErr = errors.Join(runErr, fmt.Errorf("panic preparing turn: %v", r))
		}
	}()
	if RunTurnBusyWindowFaultForTest != nil {
		RunTurnBusyWindowFaultForTest()
	}
	finishPreparing := m.beginPreparingContext(ctx, sess, id)
	defer finishPreparing()
	newUserTurn := !hostTurn && !in.Continuation && turnWasIdle
	defer m.beginTurnClock(ctx, id, newUserTurn)()
	ctx = curationctx.WithSession(ctx, curationctx.Session{
		SessionID:       id,
		ProjectID:       sess.ProjectID,
		OwnerPersonID:   sess.OwnerPersonID,
		Posture:         string(sess.Posture),
		Agent:           sess.AgentType,
		ParentSessionID: sess.ParentSessionID,
		ProjectDir:      sess.WorkspacePath,
	})
	m.beginCloseoutPrompt(ctx, sess, in)
	if !hostTurn {
		m.maybeQueueEditFollowUpRepeatKick(ctx, sess, text)
	}
	m.beginPromptTurn(id, m.coordinatorKickIDs(ctx, id)...)
	userPrompt := strings.TrimSpace(text)
	if !hostTurn && (strings.TrimSpace(in.Text) != "" || len(in.ArtifactIDs) > 0) {
		m.ensureCoordinatorRuntime().CoordinatorLoop().Waits.InterruptSleep(ctx, id)
	}
	promptFailed := true
	defer func() {
		if err := m.finishPromptExecution(ctx, id, promptFailed, hostTurn, closeoutID); err != nil {
			resp = nil
			runErr = errors.Join(runErr, err)
		}
	}()
	finishPromptRegistered = true
	openingMessageID, err := m.applyPromptUserTurn(ctx, id, in)
	if err != nil {
		return nil, err
	}
	if newUserTurn {
		m.anchorTurnClock(ctx, id, openingMessageID)
	}
	if err := m.store.CheckpointTurn(ctx, turnExecution.Turn.ID, turnExecution.Attempt.ID, store.TurnPhaseModel, "{}"); err != nil {
		return nil, err
	}
	if !hostTurn {
		defer m.beginPromptCuration(ctx, sess, userPrompt)()
	}
	ctx = curationctx.WithLane(ctx)
	m.publishUserTurnBusy(ctx, sess, userPrompt, hostTurn, turnWasIdle)

	execution, err := m.executePromptRun(ctx, sess, id, in, turnExecution, openingMessageID, userPrompt, hostTurn, finishPreparing)
	finalOutputID, closeoutID = execution.FinalOutputID, execution.CloseoutID
	if err != nil {
		return execution.Response, err
	}
	promptFailed = false
	return execution.Response, nil
}

type promptRunExecution struct {
	Response      *promptresult.Result
	FinalOutputID string
	CloseoutID    string
}

func (m *Manager) executePromptRun(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	in PromptInput,
	turn store.TurnExecution,
	openingMessageID string,
	userPrompt string,
	hostTurn bool,
	finishPreparing func(),
) (promptRunExecution, error) {
	var execution promptRunExecution
	assembly, err := m.assemblePromptRun(ctx, sess, sessionID, in, openingMessageID)
	if err != nil {
		return execution, err
	}
	if err := m.sealAuthorizationContext(ctx, sess, assembly.ProfileID, assembly.ToolCtx.WorkerJobID); err != nil {
		execution.Response, err = m.sealFailureResponse(ctx, sessionID, err)
		return execution, err
	}
	finishPreparing()

	result, err := m.ensureCoordinatorRuntime().RunPrompt(ctx, promptloop.PromptRunInput{
		SessionID:    sessionID,
		TurnID:       turn.Turn.ID,
		AttemptID:    turn.Attempt.ID,
		Session:      sess,
		History:      assembly.History,
		ProfileID:    assembly.ProfileID,
		UserPrompt:   userPrompt,
		HostTurn:     hostTurn,
		HostSignalID: in.hostSignalID(),
		ProseFinish:  in.ProseFinish,
		ToolCtx:      assembly.ToolCtx,
		Machine:      assembly.Machine,
	})
	if err != nil {
		return execution, mapPromptRunError(err)
	}
	execution.FinalOutputID = result.LastOutputID
	if hostTurn {
		if err := m.stampHostWaitOnlyCloseout(ctx, sessionID, result.Closeout, result.TurnTools); err != nil {
			return execution, err
		}
	}
	if err := m.afterPrompt(ctx, sessionID, assembly.ProfileID, result.TurnTools); err != nil {
		return execution, err
	}
	checkpointJSON, err := encodeTurnFinalizingCheckpoint(sessionID, result.LastAssistantID, result.LastOutputID)
	if err != nil {
		return execution, err
	}
	if err := m.store.SealTurnCloseout(ctx, store.TurnCloseoutCommit{
		TurnID: turn.Turn.ID, AttemptID: turn.Attempt.ID, OutputID: result.LastOutputID,
		Message: result.Closeout, CheckpointJSON: checkpointJSON,
	}); err != nil {
		return execution, err
	}
	if result.Closeout != nil {
		execution.CloseoutID = result.Closeout.ID
	}
	if strings.TrimSpace(result.LastAssistantID) == "" {
		return execution, nil
	}

	_ = m.compactOversizedToolResultsInSession(ctx, sess)
	if m.events != nil {
		if updated, err := m.store.Get(ctx, sessionID); err == nil {
			m.events.PublishSession(ctx, sessionProjectKey(updated), sessionID, updated.Status, result.LastAssistantContent)
		}
	}
	execution.Response = &promptresult.Result{
		MessageID: result.LastAssistantID,
	}
	return execution, nil
}

type promptRunAssembly struct {
	History   []api.Message
	ProfileID string
	Machine   inject.Machine
	ToolCtx   tools.ToolContext
}

// assemblePromptRun prepares one prompt run; openingMessageID is the user
// message the turn just recorded, or empty for a continuation.
func (m *Manager) assemblePromptRun(ctx context.Context, sess *api.Session, id string, in PromptInput, openingMessageID string) (promptRunAssembly, error) {
	var zero promptRunAssembly
	history, err := m.loadPromptHistory(ctx, sess)
	if err != nil {
		return zero, err
	}
	recorded, _ := m.recordedStanding(ctx, id)
	m.turnLoads.Restore(id, recorded, history)
	profileID := strings.TrimSpace(in.ToolProfile)
	if profileID == "" {
		profileID, err = m.promptToolProfile(ctx, sess)
		if err != nil {
			return zero, err
		}
	}
	surfaceID := m.resolvePromptSurfaceID(ctx, sess, profileID, m.applyCompactionView(ctx, sess, history))
	history, _, err = m.maybeCompact(ctx, sess, history, surfaceID)
	if err != nil {
		return zero, err
	}
	m.recordTurnSourceBrief(ctx, sess, openingMessageID)
	// Select turn additions before constructing their tool context.
	decision := m.beginTurnLoads(ctx, sess, id, in, history, surfaceID, profileID, openingMessageID)
	machine := m.CompileMachine(ctx, sess, profileID)
	tctx, err := m.buildToolContext(ctx, sess, profileID, machine)
	if err != nil {
		return zero, err
	}
	tctx.TurnWritePinRootID = strings.TrimSpace(in.WritePinRootID)
	tctx.TurnWritePinGlobs = append([]string(nil), in.WritePinGlobs...)
	tctx, err = m.EnrichWorkerToolContext(ctx, sess, tctx)
	if err != nil {
		return zero, err
	}
	m.finishTurnLoads(ctx, sess, id, tctx, decision)
	return promptRunAssembly{
		History:   history,
		ProfileID: profileID,
		Machine:   machine,
		ToolCtx:   tctx,
	}, nil
}

// beginPreparingContext publishes an idempotent activity lease.
func (m *Manager) beginPreparingContext(ctx context.Context, sess *api.Session, sessionID string) func() {
	if m == nil || m.events == nil || sess == nil || strings.TrimSpace(sessionID) == "" {
		return func() {}
	}
	event := api.ActivityEvent{
		ActivityID: uuid.NewString(),
		SessionID:  strings.TrimSpace(sessionID),
		Kind:       api.ActivityKindPreparingContext,
		Status:     api.ActivityStatusActive,
		StartedAt:  time.Now().UTC(),
	}
	m.events.PublishActivity(ctx, sessionProjectKey(sess), event.SessionID, event)
	var once sync.Once
	return func() {
		once.Do(func() {
			event.Status = api.ActivityStatusDone
			m.events.PublishActivity(context.WithoutCancel(ctx), sessionProjectKey(sess), event.SessionID, event)
		})
	}
}

func (m *Manager) stampHostWaitOnlyCloseout(ctx context.Context, sessionID string, message *api.Message, turnTools []string) error {
	if message == nil {
		return nil
	}
	visibility := message.Visibility
	stampHostWaitOnlyAssistant(message, true, turnTools)
	if message.Visibility == visibility {
		return nil
	}
	return m.updateMessage(ctx, sessionID, message.ID, *message)
}

func (m *Manager) resolvePromptSurfaceID(ctx context.Context, sess *api.Session, profileID string, history []api.Message) string {
	if m == nil || sess == nil || !guard.IsCoordinatorProfile(profileID) {
		return ""
	}
	var runCtx api.CoordinatorRunContext
	if m.coordinatorFrame != nil {
		if frame, err := m.coordinatorFrame.BuildCoordinatorTurnFrame(ctx, sess.ID, sess); err == nil {
			runCtx = frame.RunContext
		}
	}
	implState := m.BuildImplementSessionState(ctx, sess)
	return surface.ResolveTurnProfile(runCtx, sess, history, implState).SurfaceID
}
