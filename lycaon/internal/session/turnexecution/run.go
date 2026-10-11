package turnexecution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/promptresult"
	sessionexecution "github.com/lycaon/lycaon/internal/session/execution"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/spendguard"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/pkg/api"
)

// RunTurnBusyWindowFaultForTest injects a panic after the busy transition.
var RunTurnBusyWindowFaultForTest func()

func (m *Service) Run(ctx context.Context, id string, in promptinput.Input) (resp *promptresult.Result, runErr error) {
	ctx = m.Execution.Attach(ctx, id)
	defer m.Execution.Cancel(id)

	sess, err := m.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	releaseRuntime, err := m.Execution.BeginProject(sess.ProjectID)
	if err != nil {
		return nil, err
	}
	defer releaseRuntime()
	if m.llmSvc != nil && m.llmSvc.Policy != nil {
		policy, policyErr := m.llmSvc.Policy.GetForProjectRoots(m.Workspace.SettingsRoots(ctx, sess))
		if policyErr != nil {
			return nil, policyErr
		}
		ctx = llm.WithThinkingPolicy(ctx, policy)
	}

	if m.grounding != nil && m.grounding.IsEscalated(id) {
		return nil, guidance.ErrGroundingEscalated
	}

	if err := m.Spend.Check(ctx, id, sess); err != nil {
		if errors.Is(err, spendguard.ErrCeiling) {
			return nil, errors.Join(err, m.Transcript.PreserveBlockedPrompt(context.WithoutCancel(ctx), id, in))
		}
		slog.WarnContext(ctx, "check spend ceiling", "session_id", id, "error", err)
	}

	text := in.UserInstruction()
	hostTurn := in.HostSignal != nil
	if !hostTurn && in.Recovery == nil && m.slash != nil {
		if resp, handled, err := m.slash.TrySlashPrompt(ctx, id, text, in.SubmissionID); handled {
			return resp, mapPromptRunError(err)
		}
	}
	if !hostTurn && in.Recovery == nil && m.requests != nil {
		prepared, workflowResp, handled, err := m.requests.PrepareUserRequest(ctx, id, text)
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
	if err := m.assertRunnable(ctx, id); err != nil {
		return nil, err
	}
	turnExecution, err := m.Turns.Begin(ctx, sess, in)
	if err != nil {
		return nil, err
	}
	defer m.Execution.ClaimTurn(sess.ProjectID, turnExecution.Turn.ID)()
	finishPromptExecution := m.Coordinator.CoordinatorLoop().Admission.BeginPromptExecution(ctx, id)
	defer finishPromptExecution()
	ctx = workercontext.WithJob(ctx, in.WorkerJobID)
	finalOutputID, closeoutID := "", ""
	defer func() {
		if err := m.Turns.Finish(context.WithoutCancel(ctx), turnExecution, finalOutputID, resp, runErr); err != nil {
			resp = nil
			runErr = errors.Join(runErr, err)
		}
	}()

	if err := m.Chats.UnarchiveOnPrompt(ctx, sess); err != nil {
		return nil, err
	}
	turnWasIdle := sess.Status != api.SessionStatusBusy
	if turnWasIdle {
		if err := m.store.SetSessionStatus(ctx, id, api.SessionStatusBusy); err != nil {
			return nil, err
		}
	}
	m.SubmissionState.MarkTurnBegan(ctx)
	finishPromptRegistered := false
	defer func() {
		if finishPromptRegistered {
			return
		}
		if err := m.Settlement.Settle(context.WithoutCancel(ctx), id, m.Settlement.Disposition(true)); err != nil {
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
	finishPreparing := m.Status.Preparing(ctx, sess, id)
	defer finishPreparing()
	newUserTurn := !hostTurn && !in.Continuation && turnWasIdle
	defer m.Clocks.Begin(ctx, id, newUserTurn)()
	ctx = curationctx.WithSession(ctx, curationctx.Session{
		SessionID:       id,
		ProjectID:       sess.ProjectID,
		OwnerPersonID:   sess.OwnerPersonID,
		Posture:         string(sess.Posture),
		Agent:           sess.AgentType,
		ParentSessionID: sess.ParentSessionID,
		ProjectDir:      sess.WorkspacePath,
	})
	m.Closeouts.BeginPrompt(ctx, sess, in)
	if !hostTurn {
		m.PostTurn.EditFollowUp(ctx, sess, text)
	}
	m.Settlement.Begin(id, m.Guidance.PendingIDs(ctx, id)...)
	userPrompt := strings.TrimSpace(text)
	if !hostTurn && (strings.TrimSpace(in.Text) != "" || len(in.ArtifactIDs) > 0) {
		m.Coordinator.CoordinatorLoop().Waits.InterruptSleep(ctx, id)
	}
	promptFailed := true
	defer func() {
		if err := m.Settlement.Finish(ctx, id, promptFailed, hostTurn, closeoutID); err != nil {
			resp = nil
			runErr = errors.Join(runErr, err)
		}
	}()
	finishPromptRegistered = true
	openingMessageID, err := m.Instructions.Apply(ctx, id, in)
	if err != nil {
		return nil, err
	}
	if newUserTurn {
		m.Clocks.Anchor(ctx, id, openingMessageID)
	}
	if err := m.store.CheckpointTurn(ctx, turnExecution.Turn.ID, turnExecution.Attempt.ID, store.TurnPhaseModel, "{}"); err != nil {
		return nil, err
	}
	if !hostTurn {
		defer m.Curation.Begin(ctx, sess, userPrompt)()
	}
	ctx = curationctx.WithLane(ctx)
	m.Status.PublishBusy(ctx, sess, userPrompt, hostTurn, turnWasIdle)

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

func (m *Service) executePromptRun(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	in promptinput.Input,
	turn store.TurnExecution,
	openingMessageID string,
	userPrompt string,
	hostTurn bool,
	finishPreparing func(),
) (promptRunExecution, error) {
	var execution promptRunExecution
	assembly, err := m.Preparation.Build(ctx, sess, sessionID, in, openingMessageID)
	if err != nil {
		return execution, err
	}
	if err := m.Authorization.Seal(ctx, sess, assembly.ProfileID, assembly.ToolCtx.Identity.WorkerJobID); err != nil {
		m.Guidance.Emit(ctx, sessionID, anchor.AuthzSealFailed, anchor.Envelope{})
		execution.Response = nil
		return execution, err
	}
	finishPreparing()

	result, err := m.Coordinator.RunPrompt(ctx, promptloop.PromptRunInput{
		SessionID:    sessionID,
		TurnID:       turn.Turn.ID,
		AttemptID:    turn.Attempt.ID,
		Session:      sess,
		History:      assembly.History,
		ProfileID:    assembly.ProfileID,
		UserPrompt:   userPrompt,
		HostTurn:     hostTurn,
		HostSignalID: in.HostSignalID(),
		ProseFinish:  in.ProseFinish,
		ToolCtx:      assembly.ToolCtx,
		Machine:      assembly.Machine,
	})
	if err != nil {
		return execution, mapPromptRunError(err)
	}
	execution.FinalOutputID = result.LastOutputID
	if hostTurn {
		if err := m.Transcript.StampHostWaitCloseout(ctx, sessionID, result.Closeout, result.TurnTools); err != nil {
			return execution, err
		}
	}
	if err := m.PostTurn.After(ctx, sessionID, assembly.ProfileID, result.TurnTools); err != nil {
		return execution, err
	}
	checkpointJSON, err := sessionexecution.FinalizingCheckpoint(sessionID, result.LastAssistantID, result.LastOutputID)
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

	_ = m.History.ScheduleChunks(ctx, sess)
	m.Status.PublishAssistant(ctx, sessionID, result.LastAssistantContent)
	execution.Response = &promptresult.Result{
		MessageID: result.LastAssistantID,
	}
	return execution, nil
}
