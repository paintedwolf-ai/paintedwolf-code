package workerlane

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workspace"
)

// Runtime is Milestone 14's view of a lane at delivery: whether anything
// still writes to it, and publishing its checkpoint under the adapter's
// barrier. Delivery keeps the view; releasing it is retention's job.
type Runtime interface {
	HoldersUnresolved(ctx context.Context, root string) (bool, error)
	PublishCheckpoint(ctx context.Context, root string) (checkpointID string, err error)
}

// ErrSettlementBlocked reports a lane that still has a writer, needs
// recovery, or could not seal its checkpoint. The round stays unsettled and
// its work recoverable; no result is published from unsealed state.
var ErrSettlementBlocked = errors.New("lane settlement is blocked")

// SettleRequest settles one round after a valid report.
type SettleRequest struct {
	Actor    workerrecords.Actor
	Op       workerrecords.Op
	WorkerID string
	JobID    string
	// Publish commits the immutable result and the job's terminal transition
	// once the checkpoint boundary holds. It must be idempotent.
	Publish func(ctx context.Context, checkpointID string) error
}

// Settled reports a settled round.
type Settled struct {
	Ordinal      int64  `json:"ordinal"`
	CheckpointID string `json:"checkpoint_id,omitempty"`
}

// Settle runs lifecycle §3 for a round: account for the lane's writers,
// publish its checkpoint, publish the result, then link the round to the
// checkpoint. The lane stays live for the worker's next round.
// Each step is safe to repeat after a crash; a settled round returns its
// settlement.
func (s *Service) Settle(ctx context.Context, runtime Runtime, req SettleRequest) (Settled, error) {
	unlock := s.lock(req.WorkerID)
	defer unlock()
	if req.Publish == nil {
		return Settled{}, fmt.Errorf("settle %s: no result publisher", req.JobID)
	}
	rounds, err := s.records.Rounds(ctx, req.Actor, req.WorkerID)
	if err != nil {
		return Settled{}, err
	}
	var round *workerrecords.Round
	for i := range rounds {
		if rounds[i].JobID == strings.TrimSpace(req.JobID) {
			round = &rounds[i]
		}
	}
	if round == nil {
		return Settled{}, workerrecords.ErrRoundNotFound
	}
	if round.SettledAt != "" {
		return Settled{Ordinal: round.Ordinal, CheckpointID: round.CheckpointID}, nil
	}
	checkpointID := ""
	lane, hasLane, err := s.current(ctx, req.Actor, req.WorkerID)
	if err != nil {
		return Settled{}, err
	}
	if hasLane {
		unresolved, err := runtime.HoldersUnresolved(ctx, lane.Root)
		if err != nil {
			return Settled{}, fmt.Errorf("%w: %w", ErrSettlementBlocked, err)
		}
		if unresolved {
			return Settled{}, ErrSettlementBlocked
		}
		if checkpointID, err = runtime.PublishCheckpoint(ctx, lane.Root); err != nil {
			return Settled{}, fmt.Errorf("%w: publish checkpoint: %w", ErrSettlementBlocked, err)
		}
	}
	if err := req.Publish(ctx, checkpointID); err != nil {
		return Settled{}, fmt.Errorf("publish result: %w", err)
	}
	settled, err := s.records.SettleRound(ctx, req.Actor, req.Op, req.WorkerID, req.JobID, checkpointID)
	if err != nil {
		return Settled{}, err
	}
	return Settled{Ordinal: settled.Ordinal, CheckpointID: settled.CheckpointID}, nil
}

// ManagerRuntime reads and seals lanes through Milestone 14.
type ManagerRuntime struct{ State M14State }

// HoldersUnresolved reports a lane that still runs commands or needs recovery.
func (r ManagerRuntime) HoldersUnresolved(ctx context.Context, root string) (bool, error) {
	recovery, holders, err := r.State.Readiness(ctx, root)
	return recovery || holders, err
}

// PublishCheckpoint publishes the view's private changes and returns the
// checkpoint the branch last published.
func (r ManagerRuntime) PublishCheckpoint(ctx context.Context, root string) (string, error) {
	if _, err := workspace.PublishBranchCheckpoint(ctx, root); err != nil {
		return "", err
	}
	_, checkpointID, _ := workspace.BranchCheckpoint(root)
	return checkpointID, nil
}
