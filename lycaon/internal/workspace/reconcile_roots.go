package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// ResolveDispatchWorkspaceSelection validates an explicit adapter.WorkspaceSelection against attached
// project roots, narrowing the materialized/visible roots and subroots while keeping informational
// taskPathHints separate (#1658).
func ResolveDispatchWorkspaceSelection(
	attached []projectroot.RootRef,
	selection adapter.WorkspaceSelection,
	taskPathHints []string,
) ([]adapter.RootGrant, []projectroot.RootRef, error) {
	_ = taskPathHints // Informational hints never change workspace authority or materialization.
	grants := make([]adapter.RootGrant, 0, len(attached))
	for _, r := range attached {
		grants = append(grants, adapter.RootGrant{
			RootID:     r.ID,
			Label:      r.Label,
			SourcePath: r.Path,
			IsPrimary:  r.IsPrimary,
		})
	}
	narrowedGrants, err := adapter.ValidateWorkspaceSelection(grants, selection)
	if err != nil {
		return nil, nil, err
	}
	narrowedRefs := make([]projectroot.RootRef, 0, len(narrowedGrants))
	for _, g := range narrowedGrants {
		narrowedRefs = append(narrowedRefs, projectroot.RootRef{
			ID:        g.RootID,
			Label:     g.Label,
			Path:      g.SourcePath,
			IsPrimary: g.IsPrimary,
		})
	}
	return narrowedGrants, narrowedRefs, nil
}

// ReconcileSelectedSandboxRoots validates an explicit WorkspaceSelection against attached project roots
// and reconciles branches against the narrowed root set while keeping taskPathHints separate (#1658).
func ReconcileSelectedSandboxRoots(
	ctx context.Context,
	branchRoot string,
	attached []projectroot.RootRef,
	selection adapter.WorkspaceSelection,
	taskPathHints []string,
	settle SandboxSettler,
	loadRetainedJobs func(context.Context) (map[string]struct{}, error),
) (int, error) {
	_, narrowedRefs, err := ResolveDispatchWorkspaceSelection(attached, selection, taskPathHints)
	if err != nil {
		return 0, err
	}
	liveDirs := make([]string, 0, len(narrowedRefs))
	for _, r := range narrowedRefs {
		liveDirs = append(liveDirs, r.Path)
	}
	return ReconcileSandboxRoots(ctx, branchRoot, liveDirs, settle, loadRetainedJobs)
}

// ReconcileSandboxRoots removes unreferenced branches of detached roots.
// Retained job trees and topology remain available after a project root moves.
func ReconcileSandboxRoots(ctx context.Context, branchRoot string, liveProjectDirs []string, settle SandboxSettler, loadRetainedJobs func(context.Context) (map[string]struct{}, error)) (removed int, err error) {
	live := make(map[string]struct{}, len(liveProjectDirs))
	for _, dir := range liveProjectDirs {
		if strings.TrimSpace(dir) != "" {
			live[enginepaths.ProjectKey(dir)] = struct{}{}
		}
	}
	return reconcileBranchRoots(ctx, branchRoot, live, settle, loadRetainedJobs)
}

type detachedBranchBucket struct {
	path string
	jobs map[string]struct{}
}

func detachedBranchCandidates(ctx context.Context, branchRoot string, live map[string]struct{}) ([]detachedBranchBucket, error) {
	if strings.TrimSpace(branchRoot) == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(branchRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var candidates []detachedBranchBucket
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || !validProjectKey(entry.Name()) {
			continue
		}
		if _, ok := live[entry.Name()]; ok {
			continue
		}
		path := filepath.Join(branchRoot, entry.Name())
		children, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}
		bucket := detachedBranchBucket{path: path, jobs: make(map[string]struct{})}
		for _, child := range children {
			jobID := child.Name()
			for _, suffix := range []string{enginepaths.JobMetaDirSuffix, branchUseLockSuffix, provisionLockSuffix} {
				if strings.HasSuffix(jobID, suffix) {
					jobID = strings.TrimSuffix(jobID, suffix)
					break
				}
			}
			if jobID != "" {
				bucket.jobs[jobID] = struct{}{}
			}
		}
		candidates = append(candidates, bucket)
	}
	return candidates, nil
}

func reconcileBranchRoots(ctx context.Context, branchRoot string, live map[string]struct{}, settle SandboxSettler, loadRetainedJobs func(context.Context) (map[string]struct{}, error)) (int, error) {
	if loadRetainedJobs == nil {
		return 0, nil
	}
	candidates, err := detachedBranchCandidates(ctx, branchRoot, live)
	if err != nil || len(candidates) == 0 {
		return 0, err
	}
	// Jobs exist before provisioning. Loading ownership after enumeration protects
	// a branch published during the scan; later branches are not candidates.
	retainedJobs, err := loadRetainedJobs(ctx)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, bucket := range candidates {
		changed, err := reconcileDetachedBranchBucket(ctx, bucket, settle, retainedJobs)
		if err != nil {
			return removed, err
		}
		if changed {
			removed++
		}
	}
	return removed, nil
}

func reconcileDetachedBranchBucket(ctx context.Context, bucket detachedBranchBucket, settle SandboxSettler, retainedJobs map[string]struct{}) (bool, error) {
	changed := false
	var blocked []error
	for jobID := range bucket.jobs {
		if err := ctx.Err(); err != nil {
			return changed, err
		}
		if _, keep := retainedJobs[jobID]; keep {
			continue
		}
		root := filepath.Join(bucket.path, jobID)
		if err := settleBeforeRemoval(ctx, settle, jobID, root); err != nil {
			blocked = append(blocked, err)
			continue
		}
		removed, err := removeUnownedBranch(ctx, root)
		if err != nil {
			return changed, errors.Join(append(blocked, err)...)
		}
		changed = changed || removed
	}
	entries, err := os.ReadDir(bucket.path)
	if err != nil {
		return changed, errors.Join(append(blocked, err)...)
	}
	if len(entries) == 0 {
		return true, os.Remove(bucket.path)
	}
	return changed, errors.Join(blocked...)
}

func removeUnownedBranch(ctx context.Context, root string) (bool, error) {
	releaseUse, err := tryExclusiveBranchUse(root)
	if errors.Is(err, ErrBranchInUse) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer releaseUse()
	releaseProvision, err := tryWorkspaceProvisionLock(root)
	if errors.Is(err, ErrBranchInUse) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer releaseProvision()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return true, removeJobSandbox(root)
}
