package workspace

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/enginepaths"
)

// ReconcileSandboxRoots removes unreferenced branches and seeds of detached roots.
// Retained job trees and topology remain available after a project root moves.
func ReconcileSandboxRoots(ctx context.Context, branchRoot, seedRoot string, liveProjectDirs []string, loadRetainedJobs func(context.Context) (map[string]struct{}, error)) (removed int, err error) {
	live := make(map[string]struct{}, len(liveProjectDirs))
	for _, dir := range liveProjectDirs {
		if strings.TrimSpace(dir) != "" {
			live[enginepaths.ProjectKey(dir)] = struct{}{}
		}
	}
	var errs []error
	branches, branchErr := reconcileBranchRoots(ctx, branchRoot, live, loadRetainedJobs)
	removed += branches
	if branchErr != nil {
		errs = append(errs, branchErr)
	}
	seeds, seedErr := reconcileSeedRoots(ctx, seedRoot, live)
	removed += seeds
	if seedErr != nil {
		errs = append(errs, seedErr)
	}
	return removed, errors.Join(errs...)
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
		if !entry.IsDir() || !validSeedKey(entry.Name()) {
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

func reconcileBranchRoots(ctx context.Context, branchRoot string, live map[string]struct{}, loadRetainedJobs func(context.Context) (map[string]struct{}, error)) (int, error) {
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
		changed, err := reconcileDetachedBranchBucket(ctx, bucket, retainedJobs)
		if err != nil {
			return removed, err
		}
		if changed {
			removed++
		}
	}
	return removed, nil
}

func reconcileDetachedBranchBucket(ctx context.Context, bucket detachedBranchBucket, retainedJobs map[string]struct{}) (bool, error) {
	changed := false
	for jobID := range bucket.jobs {
		if err := ctx.Err(); err != nil {
			return changed, err
		}
		if _, keep := retainedJobs[jobID]; keep {
			continue
		}
		removed, err := removeUnownedBranch(ctx, filepath.Join(bucket.path, jobID))
		if err != nil {
			return changed, err
		}
		changed = changed || removed
	}
	entries, err := os.ReadDir(bucket.path)
	if err != nil {
		return changed, err
	}
	if len(entries) == 0 {
		return true, os.Remove(bucket.path)
	}
	return changed, nil
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

func reconcileSeedRoots(ctx context.Context, seedRoot string, live map[string]struct{}) (int, error) {
	seedRoot = strings.TrimSpace(seedRoot)
	if seedRoot == "" {
		return 0, nil
	}
	candidates, err := seedEvictionCandidates(seedRoot, "")
	if err != nil {
		return 0, fmt.Errorf("read worker seeds: %w", err)
	}
	removed := 0
	var errs []error
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if _, ok := live[candidate.key]; ok {
			continue
		}
		if err := RemoveSeedByID(ctx, seedRoot, candidate.key); err != nil {
			errs = append(errs, fmt.Errorf("remove orphan worker seed %s: %w", candidate.key, err))
			continue
		}
		removed++
		slog.InfoContext(ctx, "removed worker seed of an unregistered project", "path", candidate.path)
	}
	return removed, errors.Join(errs...)
}
