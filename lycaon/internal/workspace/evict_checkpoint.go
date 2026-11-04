package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// ErrBranchRetained reports a branch that holds the only copy of a result or has unsettled
// runtime. Retention steps aside; it is not a sweep failure.
var ErrBranchRetained = errors.New("worker branch is retained")

// EvictWorkerBranch is the retention path. A worker with a recovery journal evicts only from a
// complete verified checkpoint; any other tree evicts on its sealed overlay record.
func (m *Manager) EvictWorkerBranch(ctx context.Context, branchRoot string) error {
	jobID := jobIDForBranch(branchRoot)
	if m.recovery == nil {
		return EvictJobTree(ctx, branchRoot)
	}
	journal, err := m.recovery.LoadWorkerJournal(jobID)
	if errors.Is(err, os.ErrNotExist) {
		return EvictJobTree(ctx, branchRoot)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBranchRetained, err)
	}
	idents, err := m.recovery.ListMountIdentities(jobID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBranchRetained, err)
	}
	if len(idents) > 0 {
		return fmt.Errorf("%w: %d recorded views of %s have not settled", ErrBranchRetained, len(idents), jobID)
	}
	if _, err := EvictVerifiedCheckpointBranch(ctx, branchRoot, m.recovery, jobID, journal.ViewID, journal.BackingDir); err != nil {
		if errors.Is(err, recovery.ErrEvictUnsealedWorkRefused) {
			return fmt.Errorf("%w: %w", ErrBranchRetained, err)
		}
		return err
	}
	return nil
}

// EvictVerifiedCheckpointBranch evicts a worker branch tree and disposable working material only
// when no reader holds the branch and a complete, verified checkpoint in coord can rebuild it.
// The lease is taken first, so a refusal leaves a reader's working material untouched.
func EvictVerifiedCheckpointBranch(ctx context.Context, branchRoot string, coord *recovery.Coordinator, workerID, viewID, backingDir string) (*recovery.EvictionReport, error) {
	if coord == nil {
		return nil, fmt.Errorf("evict checkpoint branch: coordinator is required")
	}
	branchRoot, err := cleanBranchRoot(branchRoot)
	if err != nil {
		return nil, err
	}
	releaseUse, err := tryExclusiveBranchUse(branchRoot)
	if err != nil {
		return nil, err
	}
	defer releaseUse()
	if err := RefuseMountedBranch(branchRoot); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBranchRetained, err)
	}
	rep, err := coord.EvictDisposableWorkingMaterial(ctx, workerID, viewID, backingDir)
	if err != nil {
		return nil, err
	}
	return rep, removeEvictedTree(ctx, branchRoot)
}

// RebuildWorkerWorkspaceFromCheckpoint recreates an evicted branch tree at branchRoot and
// reconstructs its state from the worker's last validated published checkpoint.
func (m *Manager) RebuildWorkerWorkspaceFromCheckpoint(ctx context.Context, branchRoot string, coord *recovery.Coordinator, req recovery.RecoverRequest) (*recovery.RecoveryResult, SandboxLayout, error) {
	if coord == nil {
		return nil, SandboxLayout{}, fmt.Errorf("rebuild checkpoint branch: coordinator is required")
	}
	if err := RefuseMountedBranch(branchRoot); err != nil {
		return nil, SandboxLayout{}, err
	}
	var recRes *recovery.RecoveryResult
	layout, err := m.RebuildWorkerWorkspace(ctx, branchRoot, func(rCtx context.Context, layout SandboxLayout, root string) (rErr error) {
		if err := fillRecoveryTargets(&req.PrepareReq, layout, root); err != nil {
			return err
		}
		recRes, rErr = coord.RecoverWorkerView(rCtx, req)
		return rErr
	})
	return recRes, layout, err
}

// fillRecoveryTargets maps each root to its place in the branch, as the original claim did.
func fillRecoveryTargets(req *adapter.PrepareRequest, layout SandboxLayout, root string) error {
	if req.TargetRootDirs == nil {
		req.TargetRootDirs = make(map[string]string, len(layout.Roots))
	}
	for _, r := range layout.Roots {
		if req.TargetRootDirs[r.ID] != "" {
			continue
		}
		if len(layout.Roots) == 1 {
			req.TargetRootDirs[r.ID] = root
			continue
		}
		dir, err := projectroot.BranchDirForID(r.ID)
		if err != nil {
			return err
		}
		req.TargetRootDirs[r.ID] = filepath.Join(root, dir)
	}
	if len(req.Grants) == 0 {
		for _, r := range layout.Roots {
			label := r.Label
			if label == "" {
				label = r.ID
			}
			req.Grants = append(req.Grants, adapter.RootGrant{RootID: r.ID, Label: label, SourcePath: r.Path, IsPrimary: r.IsPrimary})
		}
	}
	return nil
}
