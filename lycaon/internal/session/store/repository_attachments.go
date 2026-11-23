package store

import (
	"context"
	"time"
)

// AttachmentRepository persists prompt attachment inventory and durable retention.
type AttachmentRepository interface {
	DeletePromptAttachmentBlob(ctx context.Context, projectID, blobID string) error
	ListOperationPromptAttachmentRetentions(ctx context.Context, operationID string) ([]PromptAttachmentRetention, error)
	ListPromptAttachmentReclaimCandidates(
		ctx context.Context, projectID string, createdBefore time.Time, limit int,
	) ([]string, error)
	PromptAttachmentBlobRetained(ctx context.Context, projectID, blobID string) (bool, error)
	PromptAttachmentStorageUsage(ctx context.Context, projectID string) (int64, error)
	RecordPromptAttachmentBlob(ctx context.Context, projectID, blobID string, byteSize int64) error
}
