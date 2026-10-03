package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/promptresult"
	"log/slog"
	"strings"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/promptstate"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

// PromptHostSignal is the machine identity persisted on a host turn receipt.
type PromptHostSignal struct {
	Kind api.MessageKind `json:"kind"`
	ID   string          `json:"id"`
}

// PromptInput is one user-facing or host-authored turn.
type PromptInput struct {
	SourceContext *api.SourceContext `json:"source_context,omitempty"`
	SubmissionID  string             `json:"submission_id,omitempty"`
	// SubmissionIDs binds coalesced receipts to one execution turn.
	SubmissionIDs []string `json:"-"`
	// AuthorPersonID is the sender recorded on the admission receipt.
	AuthorPersonID string `json:"-"`
	// WorkerJobID binds retry attempts to one semantic worker turn.
	WorkerJobID  string                   `json:"worker_job_id,omitempty"`
	Text         string                   `json:"text"`
	ArtifactIDs  []string                 `json:"artifact_ids,omitempty"`
	ContentParts []api.MessageContentPart `json:"content_parts,omitempty"`
	// ToolProfile overrides the session tool profile for this turn.
	ToolProfile string `json:"tool_profile,omitempty"`
	// WritePinRootID and WritePinGlobs constrain writes under one root.
	WritePinRootID string            `json:"write_pin_root_id,omitempty"`
	WritePinGlobs  []string          `json:"write_pin_globs,omitempty"`
	HostSignal     *PromptHostSignal `json:"host_signal,omitempty"`
	// ProseFinish closes the whole run.
	ProseFinish bool `json:"prose_finish,omitempty"`
	// Continuation delivers explicit user direction inside the open visible turn.
	Continuation        bool                `json:"continuation,omitempty"`
	Recovery            *api.PromptRecovery `json:"recovery,omitempty"`
	ResumesSubmissionID string              `json:"resumes_submission_id,omitempty"`
}

func promptUserInstruction(in PromptInput) string {
	if len(in.ContentParts) == 0 {
		return strings.TrimSpace(in.Text)
	}
	return strings.TrimSpace(api.MessageUserInstructionContent(api.Message{
		Role: api.MessageRoleUser, Content: in.Text,
		Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser, TrustTier: api.ContentTrustTierTrusted,
		ContentParts: in.ContentParts,
	}))
}

// Prompt runs one synchronous coordinator turn and drains host follow-ups.
func (m *Manager) Prompt(ctx context.Context, id string, text string) (*promptresult.Result, error) {
	ctx, unlockDispatch := m.lockPromptSubmissionDispatch(ctx, id)
	defer unlockDispatch()
	runCtx := context.WithValue(ctx, userPromptReceiptContextKey{}, true)
	resp, err := m.promptInput(runCtx, id, PromptInput{Text: text})
	if drainErr := m.DrainPromptSubmissions(context.WithoutCancel(ctx), id); drainErr != nil {
		return nil, errors.Join(err, drainErr)
	}
	return resp, err
}

// PromptWorker runs the turn bound to a worker job.
func (m *Manager) PromptWorker(ctx context.Context, id, jobID, text string) (*promptresult.Result, error) {
	ctx, unlockDispatch := m.lockPromptSubmissionDispatch(ctx, id)
	defer unlockDispatch()
	resp, err := m.promptInput(ctx, id, PromptInput{Text: text, WorkerJobID: strings.TrimSpace(jobID)})
	if drainErr := m.DrainPromptSubmissions(context.WithoutCancel(ctx), id); drainErr != nil {
		return nil, errors.Join(err, drainErr)
	}
	return resp, err
}

// PromptWorkerResume delivers a host-stamped wait winner without replaying the assignment.
func (m *Manager) PromptWorkerResume(ctx context.Context, id, jobID, leaseID string, winner awaitstore.Condition, onAdmitted func() error) (*promptresult.Result, error) {
	return m.runHostTurnWithAdmission(ctx, id, store.PromptSubmissionOriginLoopWake, PromptInput{
		SubmissionID: strings.TrimSpace(leaseID), WorkerJobID: strings.TrimSpace(jobID), Text: waitWinnerText(winner),
		HostSignal: &PromptHostSignal{Kind: api.MessageKindHostLoopWake, ID: string(anchor.LoopWake)},
	}, onAdmitted)
}

func (m *Manager) promptWaitResume(ctx context.Context, id string, delivery loopwake.WaitDelivery) (*promptresult.Result, error) {
	// Asynchronous delivery acquires its own dispatch lane.
	ctx = context.WithValue(ctx, promptSubmissionDispatchContextKey{}, "")
	ctx = context.WithValue(ctx, promptSubmissionDispatchLoopContextKey{}, false)
	ctx = context.WithValue(ctx, userPromptReceiptContextKey{}, false)
	ctx, release := m.lockPromptSubmissionDispatch(ctx, id)
	defer release()
	if !delivery.Pending() {
		return nil, nil
	}
	return m.runHostTurnWithAdmission(ctx, id, store.PromptSubmissionOriginLoopWake, PromptInput{
		SubmissionID: strings.TrimSpace(delivery.LeaseID), Text: waitWinnerText(delivery.Condition),
		HostSignal: &PromptHostSignal{Kind: api.MessageKindHostLoopWake, ID: string(anchor.LoopWake)},
	}, delivery.Admitted)
}

func waitWinnerText(winner awaitstore.Condition) string {
	text := "Wait ended: " + strings.TrimSpace(winner.Kind)
	if outcome := strings.TrimSpace(winner.Outcome); outcome != "" {
		text += " (" + outcome + ")"
	}
	if code := strings.TrimSpace(winner.Code); code != "" {
		text += ". Code: " + code
	}
	if report := strings.TrimSpace(winner.Report); report != "" {
		text += "\n\n" + report
	}
	return text
}

func (m *Manager) promptHostLoopWake(ctx context.Context, id string) (*promptresult.Result, error) {
	return m.runHostTurn(ctx, id, store.PromptSubmissionOriginLoopWake, PromptInput{
		Text: surface.HostLoopWakeSentinel,
		HostSignal: &PromptHostSignal{
			Kind: api.MessageKindHostLoopWake,
			ID:   string(anchor.LoopWake),
		},
	})
}

func (m *Manager) promptInput(ctx context.Context, id string, in PromptInput) (*promptresult.Result, error) {
	ctx, unlockDispatch := m.lockPromptSubmissionDispatch(ctx, id)
	defer unlockDispatch()
	lock := m.promptState.Prompt.Acquire(id)
	lock.Lock()
	return m.promptInputLocked(ctx, id, in, lock)
}

// promptInputLocked releases the held session lock on every return path.
func (m *Manager) promptInputLocked(ctx context.Context, id string, in PromptInput, lock *promptstate.Lock) (*promptresult.Result, error) {
	turn, err := m.captureSessionTurn(ctx, id)
	if err != nil {
		lock.Unlock()
		return nil, err
	}
	if err := m.CheckWorktreeReady(ctx, id); err != nil {
		lock.Unlock()
		return nil, err
	}
	return m.runTurnAndDrain(ctx, id, in, lock, turn)
}

// runTurnAndDrain releases the session lock before host follow-ups run.
func (m *Manager) runTurnAndDrain(ctx context.Context, id string, in PromptInput, lock *promptstate.Lock, turn lifecycle.Turn) (*promptresult.Result, error) {
	resp, err := func() (*promptresult.Result, error) {
		defer lock.Unlock()
		return m.runTurnLocked(ctx, id, in)
	}()
	m.logTurnFailure(ctx, id, err)
	err = m.reportTurnFailure(ctx, id, err)
	if m.stopState.MayDrain(turn) {
		if drainErr := m.drainPendingLoopWakes(ctx, id); drainErr != nil {
			err = errors.Join(err, drainErr)
		}
		m.maybeRunPromotion(ctx, id)
	}
	// Drain a Send reserved after the loop's final boundary check.
	if m.queue != nil && m.queue.Snapshot(id).Sending {
		if drainErr := m.DrainPromptSubmissions(context.WithoutCancel(ctx), id); drainErr != nil {
			err = errors.Join(err, drainErr)
		}
	}
	return resp, err
}

// logTurnFailure records unexpected failures at the shared drain point.
func (m *Manager) logTurnFailure(ctx context.Context, sessionID string, err error) {
	if err == nil || errors.Is(err, lifecycle.ErrStopping) {
		return
	}
	slog.ErrorContext(ctx, "coordinator turn failed", "session_id", sessionID, "err", err)
}

// A held draft keeps its receipt pending until dispatch.
func (m *Manager) routeSubmissionToDraft(ctx context.Context, row *store.PromptSubmission, userPrompt string) error {
	if m == nil || m.queue == nil || row == nil {
		return nil
	}
	if err := m.WithSessionTreeAdmission(ctx, row.SessionID, func() error {
		draft := m.queue.AppendOrdered(row.SessionID, row.ID, row.SubmittedBy, strings.TrimSpace(userPrompt), row.AdmissionSeq, row.CreatedAt)
		m.publishQueue(ctx, row.SessionID, draft.Revision)
		return nil
	}); err != nil {
		return err
	}
	m.publishSessionState(context.WithoutCancel(ctx), row.SessionID)
	return nil
}

// queueRoundComplete requires an idle worker cycle and no runnable loop wakes.
func (m *Manager) queueRoundComplete(ctx context.Context, sessionID string) bool {
	if m == nil {
		return true
	}
	rt := m.ensureCoordinatorRuntime()
	if rt == nil {
		return true
	}
	loop := rt.CoordinatorLoop()
	if loop == nil {
		return true
	}
	if loop.HasPendingLoopWakes(sessionID) && !m.hostTurnBlocked(ctx, sessionID) {
		return false
	}
	return loop.WorkerCycleIsIdle(ctx, sessionID)
}

// queueHeadIsDispatchable preserves durable receipt order across draft-ineligible inputs.
func (m *Manager) queueHeadIsDispatchable(ctx context.Context, sessionID string) (bool, error) {
	queued, err := m.store.ListQueuedUserPromptSubmissions(ctx, sessionID)
	if err != nil {
		return false, fmt.Errorf("read queued prompt order before draft drain: %w", err)
	}
	if len(queued) > 0 && !m.queue.Has(sessionID, queued[0].ID) {
		// The dispatcher settles the earlier draft-ineligible receipt.
		return false, nil
	}
	return true, nil
}

// drainQueuedNextTurn atomically claims and removes one draft group.
func (m *Manager) drainQueuedNextTurn(ctx context.Context, id string) (bool, error) {
	if m == nil || m.queue == nil {
		return false, nil
	}
	var claimed []store.PromptSubmission
	_, ok, err := m.queue.TakeNextTurn(id, func(pending []api.QueueItem) error {
		ids := make([]string, len(pending))
		for i := range pending {
			ids[i] = pending[i].ID
		}
		rows, allClaimed, claimErr := m.store.ClaimPromptSubmissions(ctx, ids)
		if claimErr != nil {
			return claimErr
		}
		if !allClaimed {
			return fmt.Errorf("queued prompt turn is no longer claimable")
		}
		claimed = rows
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("claim queued prompt turn: %w", err)
	}
	if !ok {
		return false, nil
	}
	m.publishQueue(ctx, id, m.queue.Snapshot(id).Revision)
	if err := m.runQueuedTurn(context.WithoutCancel(ctx), id, claimed); err != nil {
		return true, err
	}
	return true, nil
}

// takeQueuedSend commits the reserved head group to the active loop.
func (m *Manager) takeQueuedSend(ctx context.Context, id string) ([]api.Message, error) {
	if m == nil || m.queue == nil || m.store == nil {
		return nil, nil
	}
	var applied api.Message
	_, ok, err := m.queue.TakeSendTurn(id, func(items []api.QueueItem) error {
		inputs := make([]PromptInput, 0, len(items))
		rows := make([]*store.PromptSubmission, 0, len(items))
		queuedIDs := make([]string, 0, len(items))
		for _, item := range items {
			row, getErr := m.store.GetPromptSubmission(ctx, item.ID)
			if getErr != nil {
				return getErr
			}
			in, decodeErr := decodeQueuedSubmissionInput(row)
			if decodeErr != nil {
				return decodeErr
			}
			inputs = append(inputs, in)
			switch row.Status {
			case store.PromptSubmissionQueued:
				queuedIDs = append(queuedIDs, row.ID)
			case store.PromptSubmissionRunning:
				rows = append(rows, row)
			case store.PromptSubmissionComplete:
				// Receipt settlement makes replay a no-op.
			default:
				return fmt.Errorf("queued send receipt %s is %s", row.ID, row.Status)
			}
		}
		in := coalescePromptInputs(inputs)
		var appendErr error
		applied, appendErr = m.appendUserContinuation(ctx, id, in)
		if appendErr != nil {
			return appendErr
		}
		if len(queuedIDs) > 0 {
			claimed, allClaimed, claimErr := m.store.ClaimPromptSubmissions(ctx, queuedIDs)
			if claimErr != nil {
				return claimErr
			}
			if !allClaimed {
				return fmt.Errorf("queued send receipts are no longer claimable")
			}
			for i := range claimed {
				rows = append(rows, &claimed[i])
			}
		}
		for _, row := range rows {
			if finishErr := m.finishSubmission(ctx, row, nil, nil); finishErr != nil {
				return finishErr
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("apply queued send: %w", err)
	}
	if !ok {
		return nil, nil
	}
	draft := m.queue.Snapshot(id)
	m.publishQueue(ctx, id, draft.Revision)
	return []api.Message{applied}, nil
}

// runQueuedTurn executes one atomically claimed receipt set.
func (m *Manager) runQueuedTurn(ctx context.Context, id string, claimedRows []store.PromptSubmission) error {
	ctx, unlockDispatch := m.lockPromptSubmissionDispatch(ctx, id)
	defer unlockDispatch()
	claimed := make([]*store.PromptSubmission, 0, len(claimedRows))
	inputs := make([]PromptInput, 0, len(claimedRows))
	var closeErr error
	for i := range claimedRows {
		row := &claimedRows[i]
		in, decodeErr := decodeQueuedSubmissionInput(row)
		if decodeErr != nil {
			slog.ErrorContext(ctx, "queued prompt receipt input unusable; receipt failed",
				"session_id", id, "submission_id", row.ID, "err", decodeErr)
			if finishErr := m.store.FinishPromptSubmission(context.WithoutCancel(ctx), row.ID, row.ClaimToken,
				store.PromptSubmissionFailed, "", store.PromptSubmissionFailure{Message: decodeErr.Error()}); finishErr != nil {
				closeErr = errors.Join(closeErr, finishErr)
				slog.ErrorContext(ctx, "queued prompt receipt close failed",
					"session_id", id, "submission_id", row.ID, "err", finishErr)
			}
			continue
		}
		claimed = append(claimed, row)
		inputs = append(inputs, in)
	}
	if len(inputs) == 0 {
		m.publishSessionState(context.WithoutCancel(ctx), id)
		return closeErr
	}
	runCtx := context.WithValue(ctx, userPromptReceiptContextKey{}, true)
	ids := make([]string, len(claimed))
	for i, row := range claimed {
		ids[i] = row.ID
	}
	runCtx, dispatch := withSubmissionDispatch(runCtx, ids...)
	in := coalescePromptInputs(inputs)
	resp, err := m.promptInput(runCtx, id, in)
	for _, row := range claimed {
		if finishErr := m.finishSubmission(ctx, row, resp, err); finishErr != nil {
			closeErr = errors.Join(closeErr, finishErr)
			slog.ErrorContext(ctx, "queued prompt receipt close failed",
				"session_id", id, "submission_id", row.ID, "err", finishErr)
		}
	}
	if !dispatch.began.Load() {
		m.publishSessionState(context.WithoutCancel(ctx), id)
	}
	return closeErr
}

// decodeQueuedSubmissionInput rejects fields the text draft cannot preserve.
func decodeQueuedSubmissionInput(row *store.PromptSubmission) (PromptInput, error) {
	var in PromptInput
	if err := json.Unmarshal([]byte(row.InputJSON), &in); err != nil {
		return PromptInput{}, fmt.Errorf("stored prompt input is invalid: %w", err)
	}
	if !promptRidesQueue(in) {
		return PromptInput{}, fmt.Errorf("stored prompt input cannot ride the next-turn draft")
	}
	in.AuthorPersonID = row.SubmittedBy
	return in, nil
}

// Linked draft items share one author.
func coalescePromptInputs(inputs []PromptInput) PromptInput {
	texts := make([]string, 0, len(inputs))
	submissionIDs := make([]string, 0, len(inputs))
	continuation := false
	author := ""
	for _, in := range inputs {
		if author == "" {
			author = in.AuthorPersonID
		}
		if t := strings.TrimSpace(in.Text); t != "" {
			texts = append(texts, t)
		}
		if id := strings.TrimSpace(in.SubmissionID); id != "" {
			submissionIDs = append(submissionIDs, id)
		}
		continuation = continuation || in.Continuation
	}
	return PromptInput{
		Text: strings.Join(texts, "\n\n"), SubmissionIDs: submissionIDs,
		Continuation: continuation, AuthorPersonID: author,
	}
}

func (m *Manager) publishQueue(ctx context.Context, id string, revision uint64) {
	if m == nil || m.events == nil {
		return
	}
	m.events.PublishQueue(ctx, id, revision)
}
