package workerlane

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs/claim"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// JobLookup reads a job's promotion state.
type JobLookup interface {
	Get(jobID string) (*api.WorkerTask, bool)
}

// M14State reads lane readiness from Milestone 14's claim state and landing
// state from the job's merge status.
type M14State struct {
	Jobs JobLookup
	// BranchState returns a branch's recorded claim snapshot.
	BranchState func(root string) (claim.WorkspaceStateSnapshot, bool)
}

// NewM14State reads branch state from the workspace package.
func NewM14State(jobs JobLookup) M14State {
	return M14State{Jobs: jobs, BranchState: func(root string) (claim.WorkspaceStateSnapshot, bool) {
		snap, _, ok := workspace.BranchWorkspaceState(root)
		return snap, ok
	}}
}

// PromotionUnsettled reports a landing still applying or rebasing.
func (s M14State) PromotionUnsettled(_ context.Context, jobID string) (bool, error) {
	task, ok := s.Jobs.Get(strings.TrimSpace(jobID))
	if !ok || task == nil {
		return false, nil
	}
	switch task.MergeStatus {
	case api.WorkerMergeStatusApplying, api.WorkerMergeStatusRebasing:
		return true, nil
	default:
		return false, nil
	}
}

// Readiness reports a lane that needs recovery or still runs commands.
func (s M14State) Readiness(_ context.Context, root string) (bool, bool, error) {
	snap, ok := s.BranchState(root)
	if !ok {
		return false, false, nil
	}
	recovery := snap.Readiness == claim.StateRecoveryRequired || snap.Readiness == claim.StateFailedPreparation
	return recovery, snap.Command == claim.CommandRunning, nil
}

var _ LaneState = M14State{}
