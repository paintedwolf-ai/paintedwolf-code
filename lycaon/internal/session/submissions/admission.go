package submissions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/noticeerr"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/promptstate"
	"github.com/lycaon/lycaon/internal/session/spendguard"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/submissionstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// AdmitPrompt persists a client-authored prompt before coordinator work begins.
func (m *Service) AdmitPrompt(ctx context.Context, sessionID, operationID string, requestIdentity any, in promptinput.Input) (*store.PromptSubmission, bool, error) {
	if m == nil {
		return nil, false, fmt.Errorf("prompt manager unavailable")
	}
	unlock := m.lockAdmission(sessionID)
	defer unlock()
	return m.admitPrompt(ctx, sessionID, operationID, store.PromptSubmissionOriginUser, requestIdentity, in)
}

func (m *Service) admitPrompt(
	ctx context.Context,
	sessionID, operationID string,
	origin store.PromptSubmissionOrigin,
	requestIdentity any,
	in promptinput.Input,
) (*store.PromptSubmission, bool, error) {
	if m == nil || m.store == nil {
		return nil, false, fmt.Errorf("prompt manager unavailable")
	}
	parsed, err := uuid.Parse(strings.TrimSpace(operationID))
	if err != nil {
		return nil, false, fmt.Errorf("operation_id must be a UUID")
	}
	operationID = parsed.String()
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return nil, false, err
	}
	digest, err := Digest(sessionID, operationID, requestIdentity)
	if err != nil {
		return nil, false, err
	}
	// Existing receipts retain their identity even if the ceiling changed later.
	if existing, readErr := m.store.GetPromptSubmission(ctx, operationID); readErr == nil {
		if existing.SessionID != sessionID || existing.InputDigest != digest {
			return nil, false, &store.PromptSubmissionConflictError{ID: operationID}
		}
		return existing, false, nil
	} else if !errors.Is(readErr, store.ErrPromptSubmissionNotFound) {
		return nil, false, readErr
	}
	if in.Recovery != nil {
		in, err = m.Recovery.Prepare(ctx, sessionID, in)
		if err != nil {
			return nil, false, err
		}
	}
	// Reject known limits synchronously so the caller retains its unsent draft.
	// Execution checks again because earlier queued work can consume the runway.
	if err := m.Spend.Check(ctx, sessionID, sess); err != nil {
		if errors.Is(err, spendguard.ErrCeiling) {
			return nil, false, err
		}
		slog.WarnContext(ctx, "check prompt admission spend ceiling", "session_id", sessionID, "error", err)
	}
	in.SubmissionID = operationID
	attachmentBlobIDs := make([]string, 0, len(in.ContentParts))
	for _, part := range in.ContentParts {
		if blobID := strings.TrimSpace(part.BlobID); blobID != "" {
			attachmentBlobIDs = append(attachmentBlobIDs, blobID)
		}
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, false, fmt.Errorf("encode prompt input: %w", err)
	}
	submittedBy := ""
	if origin == store.PromptSubmissionOriginUser {
		sender, err := people.Acting(ctx, m.store)
		if err != nil {
			return nil, false, err
		}
		submittedBy = sender.ID
	}
	var row *store.PromptSubmission
	var created bool
	err = m.Gate.WithSessionTreeAdmission(ctx, sessionID, func() error {
		var putErr error
		row, created, putErr = m.store.PutPromptSubmission(ctx, store.PromptSubmission{
			ID:                operationID,
			SessionID:         sessionID,
			ProjectID:         sess.ProjectID,
			InputDigest:       digest,
			InputJSON:         string(raw),
			Origin:            origin,
			AttachmentBlobIDs: attachmentBlobIDs,
			SubmittedBy:       submittedBy,
		})
		return putErr
	})
	return row, created, err
}

// ReplayPromptSubmission checks an operation before request preparation.
func (m *Service) ReplayPromptSubmission(ctx context.Context, sessionID, operationID string, requestIdentity any) (*store.PromptSubmission, bool, error) {
	if m == nil || m.store == nil {
		return nil, false, fmt.Errorf("prompt manager unavailable")
	}
	digest, err := Digest(sessionID, operationID, requestIdentity)
	if err != nil {
		return nil, false, err
	}
	row, err := m.store.GetPromptSubmission(ctx, operationID)
	if errors.Is(err, store.ErrPromptSubmissionNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if row.SessionID != sessionID || row.InputDigest != digest {
		return nil, false, &store.PromptSubmissionConflictError{ID: operationID}
	}
	return row, true, nil
}

func Digest(sessionID, operationID string, requestIdentity any) (string, error) {
	_, err := uuid.Parse(strings.TrimSpace(operationID))
	if err != nil {
		return "", fmt.Errorf("operation_id must be a UUID")
	}
	raw, err := json.Marshal(struct {
		SessionID string `json:"session_id"`
		Request   any    `json:"request"`
	}{strings.TrimSpace(sessionID), requestIdentity})
	if err != nil {
		return "", fmt.Errorf("encode prompt identity: %w", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

// ErrInFlight reports a replay without a terminal result.
var ErrInFlight = errors.New("prompt submission is still running")

type promptSubmissionDispatchContextKey struct{}
type promptSubmissionDispatchLoopContextKey struct{}
type userPromptReceiptContextKey struct{}

func DispatchHeld(ctx context.Context, sessionID string) bool {
	heldSessionID, _ := ctx.Value(promptSubmissionDispatchContextKey{}).(string)
	return heldSessionID == sessionID
}

func UserReceiptOpen(ctx context.Context) bool {
	open, _ := ctx.Value(userPromptReceiptContextKey{}).(bool)
	return open
}

func DispatchLoopActive(ctx context.Context) bool {
	active, _ := ctx.Value(promptSubmissionDispatchLoopContextKey{}).(bool)
	return active
}

func (m *Service) LockDispatch(ctx context.Context, sessionID string) (context.Context, func()) {
	if DispatchHeld(ctx, sessionID) {
		return ctx, func() {}
	}
	dispatch := m.dispatch.Acquire(sessionID)
	dispatch.Lock()
	return context.WithValue(ctx, promptSubmissionDispatchContextKey{}, sessionID), dispatch.Unlock
}

// RunPromptSubmission dispatches one admitted receipt in admission order.
func (m *Service) RunPromptSubmission(ctx context.Context, submissionID string) (*promptresult.Result, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("prompt manager unavailable")
	}
	row, err := m.store.GetPromptSubmission(ctx, submissionID)
	if err != nil {
		return nil, err
	}
	if row.Status != store.PromptSubmissionQueued {
		return Result(row)
	}
	var in promptinput.Input
	if err := json.Unmarshal([]byte(row.InputJSON), &in); err != nil {
		ctx, unlock := m.LockDispatch(ctx, row.SessionID)
		defer unlock()
		return m.dispatchPromptSubmissions(ctx, row.SessionID, row.ID)
	}
	held := DispatchHeld(ctx, row.SessionID)
	var dispatch *promptstate.Lock
	if !held {
		dispatch = m.dispatch.Acquire(row.SessionID)
	}
	if in.RidesQueue() && !held && !dispatch.TryLock() {
		if err := m.RouteDraft(ctx, row, in.Text); err != nil {
			return nil, err
		}
		return &promptresult.Result{}, nil
	}
	if !held && !in.RidesQueue() {
		dispatch.Lock()
	}
	if !held {
		defer dispatch.Unlock()
		ctx = context.WithValue(ctx, promptSubmissionDispatchContextKey{}, row.SessionID)
	}
	return m.dispatchPromptSubmissions(ctx, row.SessionID, row.ID)
}

// DrainPromptSubmissions resumes queued receipts in admission order.
func (m *Service) DrainPromptSubmissions(ctx context.Context, sessionID string) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("prompt manager unavailable")
	}
	ctx, unlock := m.LockDispatch(ctx, sessionID)
	defer unlock()
	_, err := m.dispatchPromptSubmissions(ctx, sessionID, "")
	return err
}

func (m *Service) dispatchPromptSubmissions(ctx context.Context, sessionID, targetID string) (*promptresult.Result, error) {
	ctx = context.WithValue(ctx, promptSubmissionDispatchLoopContextKey{}, true)
	var targetFailure error
	for {
		queued, err := m.store.ListQueuedUserPromptSubmissions(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		if len(queued) == 0 {
			if targetID == "" {
				return nil, nil
			}
			if targetFailure != nil {
				return nil, targetFailure
			}
			row, getErr := m.store.GetPromptSubmission(ctx, targetID)
			if getErr != nil {
				return nil, getErr
			}
			return Result(row)
		}
		if err := m.routeWaitingQueueableSubmissions(ctx, queued[1:]); err != nil {
			return nil, err
		}
		force := m.queue != nil && m.queue.Snapshot(sessionID).Sending
		if !force && !m.roundComplete(ctx, sessionID) {
			if err := m.routeWaitingQueueableSubmissions(ctx, queued); err != nil {
				return nil, err
			}
			return m.waitingPromptSubmissionResult(ctx, targetID)
		}
		head := &queued[0]
		if m.queue != nil && m.queue.Has(sessionID, head.ID) {
			dispatchable, dispatchErr := m.HeadDispatchable(ctx, sessionID)
			if dispatchErr != nil {
				return nil, dispatchErr
			}
			if !dispatchable {
				return m.waitingPromptSubmissionResult(ctx, targetID)
			}
			progressed, drainErr := m.DrainNext(ctx, sessionID)
			if drainErr != nil {
				return nil, drainErr
			}
			if progressed {
				continue
			}
			return m.waitingPromptSubmissionResult(ctx, targetID)
		}
		_, deferred, runErr := m.runOrderedPromptSubmission(ctx, head)
		if runErr != nil {
			if head.ID == targetID {
				targetFailure = runErr
			}
			stored, getErr := m.store.GetPromptSubmission(ctx, head.ID)
			if getErr != nil {
				return nil, errors.Join(runErr, getErr)
			}
			if !stored.Status.Terminal() {
				return nil, runErr
			}
			continue
		}
		if deferred {
			return &promptresult.Result{}, nil
		}
	}
}

func (m *Service) waitingPromptSubmissionResult(ctx context.Context, targetID string) (*promptresult.Result, error) {
	if targetID == "" {
		return nil, nil
	}
	row, err := m.store.GetPromptSubmission(ctx, targetID)
	if err != nil {
		return nil, err
	}
	if row.Status == store.PromptSubmissionQueued {
		return &promptresult.Result{}, nil
	}
	return Result(row)
}

func (m *Service) routeWaitingQueueableSubmissions(ctx context.Context, rows []store.PromptSubmission) error {
	for i := range rows {
		row := &rows[i]
		if m.queue != nil && m.queue.Has(row.SessionID, row.ID) {
			continue
		}
		var in promptinput.Input
		if err := json.Unmarshal([]byte(row.InputJSON), &in); err != nil || !in.RidesQueue() {
			continue
		}
		if err := m.RouteDraft(ctx, row, in.Text); err != nil {
			return err
		}
	}
	return nil
}

func (m *Service) runOrderedPromptSubmission(ctx context.Context, row *store.PromptSubmission) (*promptresult.Result, bool, error) {
	var in promptinput.Input
	if err := json.Unmarshal([]byte(row.InputJSON), &in); err != nil {
		return nil, false, m.failUndecodableSubmission(ctx, row.ID, err)
	}
	lock := m.promptLocks.Acquire(row.SessionID)
	if in.RidesQueue() {
		if !lock.TryLock() {
			if err := m.RouteDraft(ctx, row, in.Text); err != nil {
				return nil, false, err
			}
			return nil, true, nil
		}
	} else {
		lock.Lock()
	}
	resp, err := m.RunClaimedLocked(ctx, row.ID, lock)
	return resp, false, err
}

// RunClaimedLocked releases the held session lock on every return path.
func (m *Service) RunClaimedLocked(ctx context.Context, submissionID string, lock *promptstate.Lock) (*promptresult.Result, error) {
	row, claimed, err := m.store.ClaimPromptSubmission(ctx, submissionID)
	if err != nil {
		lock.Unlock()
		return nil, err
	}
	if !claimed {
		lock.Unlock()
		return Result(row)
	}
	var in promptinput.Input
	if err := json.Unmarshal([]byte(row.InputJSON), &in); err != nil {
		lock.Unlock()
		finishErr := m.store.FinishPromptSubmission(context.WithoutCancel(ctx), row.ID, row.ClaimToken, store.PromptSubmissionFailed, "", store.PromptSubmissionFailure{Message: "stored prompt input is invalid: " + err.Error()})
		if finishErr != nil {
			return nil, errors.Join(err, finishErr)
		}
		m.publishClosedWithoutTurn(context.WithoutCancel(ctx), row)
		var drainErr error
		if !DispatchLoopActive(ctx) {
			drainErr = m.DrainPromptSubmissions(context.WithoutCancel(ctx), row.SessionID)
		}
		return nil, errors.Join(err, drainErr)
	}
	in.AuthorPersonID = row.SubmittedBy
	runCtx := ctx
	if row.Origin == store.PromptSubmissionOriginUser {
		runCtx = context.WithValue(ctx, userPromptReceiptContextKey{}, true)
	}
	runCtx, dispatch := submissionstate.WithDispatch(runCtx, row.ID)
	resp, runErr := m.execute(runCtx, row.SessionID, in, lock)
	if closeErr := m.finishSubmission(ctx, row, resp, runErr); closeErr != nil {
		return nil, errors.Join(runErr, closeErr)
	}
	if !dispatch.Began() {
		m.publishClosedWithoutTurn(context.WithoutCancel(ctx), row)
	}
	if !DispatchLoopActive(ctx) && (row.Origin == store.PromptSubmissionOriginUser || !UserReceiptOpen(ctx)) {
		if drainErr := m.DrainPromptSubmissions(context.WithoutCancel(ctx), row.SessionID); drainErr != nil {
			return nil, errors.Join(runErr, drainErr)
		}
	}
	return resp, runErr
}

// Prompts closed before a turn starts have no lifecycle event.
func (m *Service) publishClosedWithoutTurn(ctx context.Context, row *store.PromptSubmission) {
	if row.Origin == store.PromptSubmissionOriginUser {
		m.SubmissionState.Publish(ctx, row.SessionID)
	}
}

// finishSubmission persists terminal state despite caller cancellation.
func (m *Service) finishSubmission(ctx context.Context, row *store.PromptSubmission, resp *promptresult.Result, runErr error) error {
	defer m.SubmissionState.Forget(row.ID)
	status := store.PromptSubmissionComplete
	var failure store.PromptSubmissionFailure
	if runErr != nil {
		status = store.PromptSubmissionFailed
		failure.Message = runErr.Error()
		// Preserve the structured code before serializing the error.
		if code, ok := noticeerr.CodeOf(runErr); ok {
			failure.Code = string(code)
		}
		if ctx.Err() != nil || errors.Is(runErr, lifecycle.ErrStopping) {
			status = store.PromptSubmissionInterrupted
		}
	}
	resultJSON := ""
	if resp != nil {
		encoded, marshalErr := json.Marshal(resp)
		if marshalErr != nil {
			return fmt.Errorf("encode prompt result: %w", marshalErr)
		}
		resultJSON = string(encoded)
	}
	return m.store.FinishPromptSubmission(context.WithoutCancel(ctx), row.ID, row.ClaimToken, status, resultJSON, failure)
}

// AbandonPromptSubmission fails only unclaimed receipts.
func (m *Service) AbandonPromptSubmission(ctx context.Context, submissionID string, cause error) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("prompt manager unavailable")
	}
	row, claimed, err := m.store.ClaimPromptSubmission(ctx, submissionID)
	if err != nil || !claimed {
		return err
	}
	failure := store.PromptSubmissionFailure{Message: cause.Error()}
	if code, ok := noticeerr.CodeOf(cause); ok {
		failure.Code = string(code)
	}
	hostCtx := context.WithoutCancel(ctx)
	if err := m.store.FinishPromptSubmission(hostCtx, row.ID, row.ClaimToken, store.PromptSubmissionFailed, "", failure); err != nil {
		return err
	}
	m.publishClosedWithoutTurn(hostCtx, row)
	return nil
}

// failUndecodableSubmission prevents recovery from retrying invalid stored input.
func (m *Service) failUndecodableSubmission(ctx context.Context, submissionID string, decodeErr error) error {
	row, claimed, err := m.store.ClaimPromptSubmission(ctx, submissionID)
	if err != nil {
		return errors.Join(decodeErr, err)
	}
	if !claimed {
		return decodeErr
	}
	finishErr := m.store.FinishPromptSubmission(context.WithoutCancel(ctx), row.ID, row.ClaimToken, store.PromptSubmissionFailed, "", store.PromptSubmissionFailure{Message: "stored prompt input is invalid: " + decodeErr.Error()})
	if finishErr == nil {
		m.publishClosedWithoutTurn(context.WithoutCancel(ctx), row)
	}
	return errors.Join(decodeErr, finishErr)
}

func Result(row *store.PromptSubmission) (*promptresult.Result, error) {
	if row == nil {
		return nil, nil
	}
	switch row.Status {
	case store.PromptSubmissionRunning:
		return nil, ErrInFlight
	case store.PromptSubmissionCanceled:
		return nil, fmt.Errorf("prompt submission %s was removed from the queue before it ran", row.ID)
	case store.PromptSubmissionInterrupted:
		// The sentinel lets receipt readers classify a stop like the runner did.
		return nil, fmt.Errorf("prompt submission %s was interrupted: %s: %w", row.ID, row.Error, lifecycle.ErrStopping)
	case store.PromptSubmissionFailed:
		failed := fmt.Errorf("prompt submission %s is %s: %s", row.ID, row.Status, row.Error)
		return nil, noticeerr.WithCode(failed, api.NoticeCode(row.ErrorCode))
	case store.PromptSubmissionQueued, store.PromptSubmissionComplete:
	}
	if row.ResultJSON == "" {
		return nil, nil
	}
	var resp promptresult.Result
	if err := json.Unmarshal([]byte(row.ResultJSON), &resp); err != nil {
		return nil, fmt.Errorf("decode prompt submission result: %w", err)
	}
	return &resp, nil
}

// RecoverPromptSubmissions resolves work interrupted by host exit.
func (m *Service) RecoverPromptSubmissions(ctx context.Context) ([]string, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("prompt manager unavailable")
	}
	return m.store.RecoverPromptSubmissions(ctx)
}

func (m *Service) GetPromptSubmission(ctx context.Context, id string) (*store.PromptSubmission, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("prompt manager unavailable")
	}
	return m.store.GetPromptSubmission(ctx, id)
}
