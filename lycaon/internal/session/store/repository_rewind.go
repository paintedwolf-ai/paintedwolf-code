package store

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// RewindRepository persists checkpoints and recoverable rewind operations.
type RewindRepository interface {
	CommitRewind(ctx context.Context, id, sessionID, anchorMessageID string, response api.RewindSessionResponse) (int, error)
	CommittedRewindOperationsForSweep(ctx context.Context) ([]RewindOperationSweepItem, error)
	DeleteRewindOperation(ctx context.Context, id string, olderThan time.Time) error
	DropCheckpoint(ctx context.Context, session, anchor string) error
	DropRootCheckpoints(ctx context.Context, projectID, root string) error
	GetRewindOperation(ctx context.Context, id string) (*RewindOperation, error)
	PrepareRewind(ctx context.Context, op RewindOperation) error
	PutCheckpoint(ctx context.Context, root, session, anchor, sealed, manifest string, objects []CheckpointObject) error
	ReadCheckpoint(ctx context.Context, root, session, anchor string) (string, error)
	ReadCheckpointForRewind(ctx context.Context, session, anchor string) (string, error)
	RewindOperationsForRecovery(ctx context.Context) ([]RewindOperation, error)
	RewindOperationsForRecoverySession(ctx context.Context, sessionID string) ([]RewindOperation, error)
	SetRewindPhase(ctx context.Context, id, status, detail string) error
}
