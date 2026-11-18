package store

import (
	"context"
)

// SubmissionRepository persists admitted prompt receipts and claims.
type SubmissionRepository interface {
	CancelQueuedPromptSubmissions(ctx context.Context, ids []string) error
	ClaimPromptSubmission(ctx context.Context, id string) (*PromptSubmission, bool, error)
	ClaimPromptSubmissions(ctx context.Context, ids []string) ([]PromptSubmission, bool, error)
	FinishPromptSubmission(ctx context.Context, id, claimToken string, status PromptSubmissionStatus, resultJSON string, failure PromptSubmissionFailure) error
	GetPromptSubmission(ctx context.Context, id string) (*PromptSubmission, error)
	InterruptPromptSubmissionsBySession(ctx context.Context, sessionID string) error
	InterruptRunningPromptSubmissionsBySession(ctx context.Context, sessionID string) error
	ListQueuedUserPromptSubmissions(ctx context.Context, sessionID string) ([]PromptSubmission, error)
	ListUnsettledUserPromptSubmissionIDs(ctx context.Context, sessionID string) ([]string, error)
	PutPromptSubmission(ctx context.Context, in PromptSubmission) (*PromptSubmission, bool, error)
	RecoverPromptSubmissions(ctx context.Context) ([]string, error)
	UpdateQueuedPromptSubmissionInputs(
		ctx context.Context,
		updates []PromptSubmissionInputUpdate,
	) (bool, error)
}
