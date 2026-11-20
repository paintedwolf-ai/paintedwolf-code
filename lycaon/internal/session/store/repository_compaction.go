package store

import (
	"context"

	"github.com/lycaon/lycaon/internal/messageview"
)

// CompactionRepository persists derived context views and their source validation.
type CompactionRepository interface {
	CompactionViewCurrent(ctx context.Context, sessionID string, coveredThroughOrd int64, coveredThroughID string, sourceSeq int64) (bool, error)
	GetChunkProjection(ctx context.Context, sessionID, messageID string) (messageview.ChunkProjection, bool, error)
	GetCompactionAttempt(ctx context.Context, sessionID string) (messageview.CompactionAttempt, bool, error)
	GetCompactionView(ctx context.Context, sessionID string) (*CompactionView, bool, error)
	PutChunkProjection(ctx context.Context, sessionID, messageID string, projection messageview.ChunkProjection) error
	PutCompactionAttempt(ctx context.Context, sessionID string, attempt messageview.CompactionAttempt) error
	PutCompactionView(ctx context.Context, sessionID string, view CompactionView) error
}
