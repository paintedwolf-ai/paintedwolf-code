package submissions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/noticeerr"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

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
	hostCtx := context.WithoutCancel(ctx)
	err := m.store.FinishPromptSubmission(hostCtx, row.ID, row.ClaimToken, status, resultJSON, failure)
	if err != nil {
		// Stop may settle the same claim before the provider unwinds.
		current, readErr := m.store.GetPromptSubmission(hostCtx, row.ID)
		if readErr == nil && current != nil && row.ClaimToken != "" &&
			current.ClaimToken == row.ClaimToken && current.Status == store.PromptSubmissionInterrupted {
			return lifecycle.ErrStopping
		}
	}
	return err
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
