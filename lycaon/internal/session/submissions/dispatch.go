package submissions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/submissionstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) Prompt(ctx context.Context, id string, text string) (*promptresult.Result, error) {
	ctx, unlockDispatch := m.LockDispatch(ctx, id)
	defer unlockDispatch()
	runCtx := context.WithValue(ctx, userPromptReceiptContextKey{}, true)
	resp, err := m.Input(runCtx, id, promptinput.Input{Text: text})
	if drainErr := m.DrainPromptSubmissions(context.WithoutCancel(ctx), id); drainErr != nil {
		return nil, errors.Join(err, drainErr)
	}
	return resp, err
}

func (m *Service) PromptWorker(ctx context.Context, id, jobID, text string) (*promptresult.Result, error) {
	ctx, unlockDispatch := m.LockDispatch(ctx, id)
	defer unlockDispatch()
	resp, err := m.Input(ctx, id, promptinput.Input{Text: text, WorkerJobID: strings.TrimSpace(jobID)})
	if drainErr := m.DrainPromptSubmissions(context.WithoutCancel(ctx), id); drainErr != nil {
		return nil, errors.Join(err, drainErr)
	}
	return resp, err
}

func (m *Service) PromptWorkerResume(ctx context.Context, id, jobID, leaseID string, winner awaitstore.Condition, onAdmitted func() error) (*promptresult.Result, error) {
	return m.HostTurnWithAdmission(ctx, id, store.PromptSubmissionOriginLoopWake, promptinput.Input{
		SubmissionID: strings.TrimSpace(leaseID), WorkerJobID: strings.TrimSpace(jobID), Text: WaitWinnerText(winner),
		HostSignal: &promptinput.HostSignal{Kind: api.MessageKindHostLoopWake, ID: string(anchor.LoopWake)},
	}, onAdmitted)
}

func (m *Service) WaitResume(ctx context.Context, id string, delivery loopwake.WaitDelivery) (*promptresult.Result, error) {
	// Asynchronous delivery acquires its own dispatch lane.
	ctx = context.WithValue(ctx, promptSubmissionDispatchContextKey{}, "")
	ctx = context.WithValue(ctx, promptSubmissionDispatchLoopContextKey{}, false)
	ctx = context.WithValue(ctx, userPromptReceiptContextKey{}, false)
	ctx, release := m.LockDispatch(ctx, id)
	defer release()
	if !delivery.Pending() {
		return nil, nil
	}
	return m.HostTurnWithAdmission(ctx, id, store.PromptSubmissionOriginLoopWake, promptinput.Input{
		SubmissionID: strings.TrimSpace(delivery.LeaseID), Text: WaitWinnerText(delivery.Condition),
		HostSignal: &promptinput.HostSignal{Kind: api.MessageKindHostLoopWake, ID: string(anchor.LoopWake)},
	}, delivery.Admitted)
}

func WaitWinnerText(winner awaitstore.Condition) string {
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

func (m *Service) LoopWake(ctx context.Context, id string) (*promptresult.Result, error) {
	return m.runHostTurn(ctx, id, store.PromptSubmissionOriginLoopWake, promptinput.Input{
		Text: surface.HostLoopWakeSentinel,
		HostSignal: &promptinput.HostSignal{
			Kind: api.MessageKindHostLoopWake,
			ID:   string(anchor.LoopWake),
		},
	})
}

func (m *Service) Input(ctx context.Context, id string, in promptinput.Input) (*promptresult.Result, error) {
	ctx, unlockDispatch := m.LockDispatch(ctx, id)
	defer unlockDispatch()
	lock := m.promptLocks.Acquire(id)
	lock.Lock()
	return m.execute(ctx, id, in, lock)
}

func (m *Service) RouteDraft(ctx context.Context, row *store.PromptSubmission, userPrompt string) error {
	if m == nil || m.queue == nil || row == nil {
		return nil
	}
	if err := m.Gate.WithSessionTreeAdmission(ctx, row.SessionID, func() error {
		draft := m.queue.AppendOrdered(row.SessionID, row.ID, row.SubmittedBy, strings.TrimSpace(userPrompt), row.AdmissionSeq, row.CreatedAt)
		m.Drafts.Publish(ctx, row.SessionID, draft.Revision)
		return nil
	}); err != nil {
		return err
	}
	m.SubmissionState.Publish(context.WithoutCancel(ctx), row.SessionID)
	return nil
}

func (m *Service) HeadDispatchable(ctx context.Context, sessionID string) (bool, error) {
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

func (m *Service) DrainNext(ctx context.Context, id string) (bool, error) {
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
	m.Drafts.Publish(ctx, id, m.queue.Snapshot(id).Revision)
	if err := m.runQueuedTurn(context.WithoutCancel(ctx), id, claimed); err != nil {
		return true, err
	}
	return true, nil
}

func (m *Service) TakeSend(ctx context.Context, id string) ([]api.Message, error) {
	if m == nil || m.queue == nil || m.store == nil {
		return nil, nil
	}
	var applied api.Message
	_, ok, err := m.queue.TakeSendTurn(id, func(items []api.QueueItem) error {
		inputs := make([]promptinput.Input, 0, len(items))
		rows := make([]*store.PromptSubmission, 0, len(items))
		queuedIDs := make([]string, 0, len(items))
		for _, item := range items {
			row, getErr := m.store.GetPromptSubmission(ctx, item.ID)
			if getErr != nil {
				return getErr
			}
			in, decodeErr := promptinput.FromSubmission(row)
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
		in := promptinput.Coalesce(inputs)
		var appendErr error
		applied, appendErr = m.continueUser(ctx, id, in)
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
	m.Drafts.Publish(ctx, id, draft.Revision)
	return []api.Message{applied}, nil
}

func (m *Service) runQueuedTurn(ctx context.Context, id string, claimedRows []store.PromptSubmission) error {
	ctx, unlockDispatch := m.LockDispatch(ctx, id)
	defer unlockDispatch()
	claimed := make([]*store.PromptSubmission, 0, len(claimedRows))
	inputs := make([]promptinput.Input, 0, len(claimedRows))
	var closeErr error
	for i := range claimedRows {
		row := &claimedRows[i]
		in, decodeErr := promptinput.FromSubmission(row)
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
		m.SubmissionState.Publish(context.WithoutCancel(ctx), id)
		return closeErr
	}
	runCtx := context.WithValue(ctx, userPromptReceiptContextKey{}, true)
	ids := make([]string, len(claimed))
	for i, row := range claimed {
		ids[i] = row.ID
	}
	runCtx, dispatch := submissionstate.WithDispatch(runCtx, ids...)
	in := promptinput.Coalesce(inputs)
	resp, err := m.Input(runCtx, id, in)
	for _, row := range claimed {
		if finishErr := m.finishSubmission(ctx, row, resp, err); finishErr != nil {
			closeErr = errors.Join(closeErr, finishErr)
			slog.ErrorContext(ctx, "queued prompt receipt close failed",
				"session_id", id, "submission_id", row.ID, "err", finishErr)
		}
	}
	if !dispatch.Began() {
		m.SubmissionState.Publish(context.WithoutCancel(ctx), id)
	}
	return closeErr
}
