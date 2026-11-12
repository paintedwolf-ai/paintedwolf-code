package workerlane

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workspace"
)

// Runtime is Milestone 14's settlement of a lane view: releasing its
// processes and mounts, and publishing its checkpoint.
type Runtime interface {
	SettleBranchRuntime(ctx context.Context, viewID, root string) error
	PublishCheckpoint(ctx context.Context, root string) (checkpointID string, err error)
}

// ErrSettlementBlocked reports a lane whose writers or resources could not be
// released. The round stays unsettled and its work recoverable; no result is
// published from unsealed state.
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

// Settle runs lifecycle §3 for a round: release the lane's runtime, publish
// its checkpoint, publish the result, then link the round to the checkpoint.
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
		viewID := laneViewID(req.WorkerID, lane.Generation)
		if err := runtime.SettleBranchRuntime(ctx, viewID, lane.Root); err != nil {
			return Settled{}, fmt.Errorf("%w: %w", ErrSettlementBlocked, err)
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

// ManagerRuntime settles lanes through a Milestone 14 workspace manager.
type ManagerRuntime struct{ Manager *workspace.Manager }

// SettleBranchRuntime releases the view's processes and mounts.
func (r ManagerRuntime) SettleBranchRuntime(ctx context.Context, viewID, root string) error {
	return r.Manager.SettleBranchRuntime(ctx, viewID, root)
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
