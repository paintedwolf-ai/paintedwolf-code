package worker

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

const maxWorkerExecutionAttempts = 3

// PermanentExecutionError prevents retries for invalid durable input.
type PermanentExecutionError struct {
	Err error
}

func (e *PermanentExecutionError) Error() string {
	if e == nil || e.Err == nil {
		return "permanent worker execution failure"
	}
	return e.Err.Error()
}

func (e *PermanentExecutionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func workerAttemptRetryable(task *api.WorkerTask, err error) bool {
	if task == nil || task.Attempt >= maxWorkerExecutionAttempts || err == nil {
		return false
	}
	var permanent *PermanentExecutionError
	var thinking *llm.ThinkingOverrideError
	if errors.As(err, &thinking) {
		return false
	}
	if errors.As(err, &permanent) || errors.Is(err, context.Canceled) || errors.Is(err, lifecycle.ErrStopping) || errors.Is(err, ErrClaimLost) {
		return false
	}
	if empty, ok := failure.AsProviderEmptyCompletion(err); ok && !empty.Retryable {
		return false
	}
	if _, ok := providerretry.AsProviderRequestRejected(err); ok {
		return false
	}
	switch {
	case errors.Is(err, failure.ErrProviderNotConfigured),
		errors.Is(err, failure.ErrProviderOutputTruncated),
		errors.Is(err, failure.ErrProviderContextTooSmall),
		errors.Is(err, failure.ErrProviderToolCallsUnsupported),
		errors.Is(err, failure.ErrProviderToolCallsInProse),
		errors.Is(err, workspace.ErrWorkspaceStorageExhausted),
		errors.Is(err, ErrWorkerBranchClaimFailed),
		errors.Is(err, promptloop.ErrOwnerUnsettled):
		return false
	default:
		return true
	}
}
