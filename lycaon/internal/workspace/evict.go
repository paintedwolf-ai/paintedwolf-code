package workspace

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"os"
)

// EvictJobTree removes a branch tree while keeping its .meta sibling, so the
// tree can be rebuilt from the job's baseline and overlay records. It refuses
// while any reader holds the branch. The meta is marked incomplete before the
// first byte is removed, so an interrupted eviction reads as an evicted tree.
func EvictJobTree(ctx context.Context, branchRoot string) error {
	branchRoot, err := cleanBranchRoot(branchRoot)
	if err != nil {
		return err
	}
	releaseUse, err := tryExclusiveBranchUse(branchRoot)
	if err != nil {
		return err
	}
	defer releaseUse()
	releaseProvision, err := acquireWorkspaceProvisionLock(ctx, branchRoot)
	if err != nil {
		return err
	}
	defer releaseProvision()
	metaDir := enginepaths.MetaDirForBranchRoot(branchRoot)
	meta, err := LoadJobMeta(metaDir)
	if err != nil {
		return fmt.Errorf("evict worker branch: %w", err)
	}
	if meta.SnapshotComplete {
		meta.SnapshotComplete = false
		if err := WriteJobMeta(metaDir, meta); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(branchRoot); err != nil {
		return fmt.Errorf("evict worker branch: %w", err)
	}
	return nil
}

// RebuildWorkerWorkspace recreates an evicted branch tree at its recorded path.
// restore fills the fresh skeleton from the job's records; the layout is the
// one captured at claim time. A tree that is already complete is left alone.
func (m *Manager) RebuildWorkerWorkspace(
	ctx context.Context,
	branchRoot string,
	restore func(ctx context.Context, layout SandboxLayout, branchRoot string) error,
) (SandboxLayout, error) {
	branchRoot, err := cleanBranchRoot(branchRoot)
	if err != nil {
		return SandboxLayout{}, err
	}
	release, err := acquireWorkspaceProvisionLock(ctx, branchRoot)
	if err != nil {
		return SandboxLayout{}, err
	}
	defer release()
	if layout, err := LoadBranchLayout(branchRoot); err == nil {
		return layout, nil
	}
	meta, err := LoadJobMeta(enginepaths.MetaDirForBranchRoot(branchRoot))
	if err != nil {
		return SandboxLayout{}, fmt.Errorf("rebuild worker branch: %w", err)
	}
	layout, err := layoutFromJobMeta(meta)
	if err != nil {
		return SandboxLayout{}, err
	}
	binding, err := m.createBranchSkeleton(ctx, branchRoot, layout, jobIDForBranch(branchRoot))
	if err != nil {
		return SandboxLayout{}, err
	}
	if err := restore(ctx, layout, binding.Root); err != nil {
		_ = os.RemoveAll(binding.Root)
		return SandboxLayout{}, err
	}
	if err := markSnapshotComplete(enginepaths.MetaDirForBranchRoot(binding.Root)); err != nil {
		return SandboxLayout{}, err
	}
	TouchBranchUse(binding.Root)
	return layout, nil
}
