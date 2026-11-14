package workerlane

import (
	"context"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/workerrecords"
)

// Checkpoints confirms a checkpoint can still be reconstructed.
type Checkpoints interface {
	VerifyCheckpointReconstructible(ctx context.Context, checkpointID string) (checkpoint.Manifest, error)
}

var (
	// ErrResultUnsettled reports a result whose round has not settled.
	ErrResultUnsettled = errors.New("result's round has not settled")
	// ErrHandleStale reports a result whose checkpoint can no longer be
	// reconstructed, so preview, promotion and rejection cannot name it.
	ErrHandleStale = errors.New("result no longer names its checkpoint")
)

// Handle names exactly what a settled round delivered.
type Handle struct {
	ResultID     string `json:"result_id"`
	WorkerID     string `json:"worker_id"`
	JobID        string `json:"job_id"`
	Ordinal      int64  `json:"ordinal"`
	CheckpointID string `json:"checkpoint_id,omitempty"`
}

// Changed reports whether the result carries changes to review or land.
func (h Handle) Changed() bool { return h.CheckpointID != "" }

// Handle resolves a result to its settled round and checkpoint. A round
// that changed nothing has a handle with no checkpoint.
func (s *Service) Handle(ctx context.Context, checkpoints Checkpoints, actor workerrecords.Actor, resultID string) (Handle, error) {
	ref, err := s.records.Result(ctx, actor, resultID)
	if err != nil {
		return Handle{}, err
	}
	rounds, err := s.records.Rounds(ctx, actor, ref.WorkerID)
	if err != nil {
		return Handle{}, err
	}
	for _, r := range rounds {
		if r.JobID != ref.JobID {
			continue
		}
		if r.SettledAt == "" {
			return Handle{}, ErrResultUnsettled
		}
		h := Handle{ResultID: ref.ResultID, WorkerID: ref.WorkerID, JobID: ref.JobID, Ordinal: r.Ordinal, CheckpointID: r.CheckpointID}
		if h.CheckpointID == "" {
			return h, nil
		}
		if _, err := checkpoints.VerifyCheckpointReconstructible(ctx, h.CheckpointID); err != nil {
			return Handle{}, fmt.Errorf("%w: %s: %w", ErrHandleStale, h.CheckpointID, err)
		}
		return h, nil
	}
	return Handle{}, workerrecords.ErrRoundNotFound
}

var _ Checkpoints = (*recovery.Coordinator)(nil)
