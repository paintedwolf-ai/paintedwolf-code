package workspace

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

// SandboxSettler proves nothing recorded can still reach a worker's data. An error
// keeps the sandbox, and the settler retains the state a later retry resumes from.
type SandboxSettler func(ctx context.Context, jobID, root string) error

// ReconcileStaleSandboxes removes branch trees whose directory name current state does not
// retain, settling each one first. A blocked candidate is kept and reported; the rest proceed.
func ReconcileStaleSandboxes(
	ctx context.Context,
	branchRoot, primaryDir string,
	settle SandboxSettler,
	loadRetainedBranches func(context.Context) (map[string]struct{}, error),
) (removed int, err error) {
	stateRoot := filepath.Dir(strings.TrimSpace(branchRoot))
	listing, err := listAllSandboxEntries(stateRoot, branchRoot, primaryDir)
	if err != nil {
		return 0, err
	}
	retainJobIDs, err := loadRetainedBranches(ctx)
	if err != nil {
		return 0, err
	}
	sort.Slice(listing.jobs, func(i, j int) bool {
		modI, errI := sandboxDirModTime(listing.jobs[i].root)
		modJ, errJ := sandboxDirModTime(listing.jobs[j].root)
		if errI != nil {
			return false
		}
		if errJ != nil {
			return true
		}
		return modI.Before(modJ)
	})
	var blocked []error
	for _, dir := range listing.jobs {
		if err := ctx.Err(); err != nil {
			return removed, errors.Join(append(blocked, err)...)
		}
		if _, keep := retainJobIDs[dir.jobID]; keep && !placedTreeSuperseded(stateRoot, dir.root) {
			continue
		}
		didRemove, blockedErr, fatalErr := reconcileStaleSandboxCandidate(ctx, stateRoot, primaryDir, dir, settle, loadRetainedBranches)
		if fatalErr != nil {
			return removed, errors.Join(append(blocked, fatalErr)...)
		}
		if blockedErr != nil {
			blocked = append(blocked, blockedErr)
			continue
		}
		if didRemove {
			removed++
		}
	}
	orphans, err := removeUnretainedOrphanMeta(ctx, stateRoot, primaryDir, listing.metas, retainJobIDs, loadRetainedBranches)
	if err != nil {
		return removed, errors.Join(append(blocked, err)...)
	}
	return removed + orphans, errors.Join(blocked...)
}

func reconcileStaleSandboxCandidate(
	ctx context.Context,
	stateRoot, primaryDir string,
	dir sandboxDir,
	settle SandboxSettler,
	loadRetainedBranches func(context.Context) (map[string]struct{}, error),
) (removed bool, blockedErr error, fatalErr error) {
	releaseLifecycle, err := acquireBranchReconcile(ctx, dir.jobID)
	if err != nil {
		return false, nil, err
	}
	defer releaseLifecycle()
	if _, err := adapter.BoundedLstat(dir.root); errors.Is(err, os.ErrNotExist) {
		return false, nil, nil
	}
	releaseUse, err := tryExclusiveBranchUse(dir.root)
	if errors.Is(err, ErrBranchInUse) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	releaseProvision, err := tryWorkspaceProvisionLock(dir.root)
	if errors.Is(err, ErrBranchInUse) {
		releaseUse()
		return false, nil, nil
	}
	if err != nil {
		releaseUse()
		return false, nil, err
	}
	releaseFileLocks := func() {
		releaseProvision()
		releaseUse()
	}
	// Revalidate eligibility under the lifecycle owner so a retention snapshot
	// taken before preparation committed cannot authorize settlement or removal.
	latestRetained, err := loadRetainedBranches(ctx)
	if err != nil {
		releaseFileLocks()
		return false, nil, err
	}
	if _, keep := latestRetained[dir.jobID]; keep && !placedTreeSuperseded(stateRoot, dir.root) {
		releaseFileLocks()
		return false, nil, nil
	}
	if err := settleBeforeRemoval(ctx, settle, dir.jobID, dir.root); err != nil {
		releaseFileLocks()
		return false, err, nil
	}
	if err := removeJobSandboxTreeAndMeta(dir.root); err != nil {
		releaseFileLocks()
		return false, nil, err
	}
	releaseFileLocks()
	removeBranchLocks(dir.root)
	if err := forgetPlacedBranch(ctx, stateRoot, dir.root); err != nil {
		return false, nil, err
	}
	slog.InfoContext(ctx, "removed stale worker sandbox",
		"project_dir", strings.TrimSpace(primaryDir),
		"job_id", dir.jobID,
		"path", dir.root,
	)
	return true, nil, nil
}

func settleBeforeRemoval(ctx context.Context, settle SandboxSettler, jobID, root string) error {
	if settle != nil {
		if err := settle(ctx, jobID, root); err != nil {
			return fmt.Errorf("settle worker %s before removal: %w", jobID, err)
		}
	}
	return RefuseMountedBranch(root)
}

// ErrBranchMounted refuses to delete through a filesystem still mounted in a branch tree.
var ErrBranchMounted = errors.New("worker branch tree is mounted")

// RefuseMountedBranch fails while a mount boundary remains at a branch root or at a per-root
// directory beneath it. It reads kernel facts, so it holds even when records are missing:
// recursive removal through a live mount would delete the worker's result.
func RefuseMountedBranch(root string) error {
	mounted, err := adapter.MountBoundaryPresent(root)
	if err != nil {
		return err
	}
	if mounted {
		return fmt.Errorf("%w: %s", ErrBranchMounted, root)
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrBranchMounted, root, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		child := filepath.Join(root, entry.Name())
		mounted, err := adapter.MountBoundaryPresent(child)
		if err != nil {
			return err
		}
		if mounted {
			return fmt.Errorf("%w: %s", ErrBranchMounted, child)
		}
	}
	return nil
}

type sandboxDir struct {
	jobID string
	root  string
}

type sandboxListing struct {
	jobs  []sandboxDir
	metas []sandboxDir
}

func listSandboxEntries(branchRoot, primaryDir string) (sandboxListing, error) {
	parent := enginepaths.ProjectBranchDir(branchRoot, primaryDir)
	entries, err := os.ReadDir(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return sandboxListing{}, nil
		}
		return sandboxListing{}, err
	}
	var listing sandboxListing
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" {
			continue
		}
		if enginepaths.IsJobMetaDirName(name) {
			jobID := strings.TrimSuffix(name, enginepaths.JobMetaDirSuffix)
			if jobID == "" {
				continue
			}
			listing.metas = append(listing.metas, sandboxDir{
				jobID: jobID,
				root:  filepath.Join(parent, name),
			})
			continue
		}
		listing.jobs = append(listing.jobs, sandboxDir{
			jobID: name,
			root:  filepath.Join(parent, name),
		})
	}
	if len(listing.metas) == 0 {
		return listing, nil
	}
	haveJob := make(map[string]struct{}, len(listing.jobs))
	for _, job := range listing.jobs {
		haveJob[job.jobID] = struct{}{}
	}
	orphans := listing.metas[:0]
	for _, meta := range listing.metas {
		if _, ok := haveJob[meta.jobID]; ok {
			continue
		}
		orphans = append(orphans, meta)
	}
	listing.metas = orphans
	return listing, nil
}

func removeUnretainedOrphanMeta(
	ctx context.Context,
	stateRoot, primaryDir string,
	metas []sandboxDir,
	retainJobIDs map[string]struct{},
	loadRetainedBranches func(context.Context) (map[string]struct{}, error),
) (int, error) {
	removed := 0
	for _, meta := range metas {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if _, keep := retainJobIDs[meta.jobID]; keep {
			continue
		}
		didRemove, err := reconcileOrphanMetaCandidate(ctx, stateRoot, primaryDir, meta, loadRetainedBranches)
		if err != nil {
			return removed, err
		}
		if didRemove {
			removed++
		}
	}
	return removed, nil
}

func reconcileOrphanMetaCandidate(
	ctx context.Context,
	stateRoot, primaryDir string,
	meta sandboxDir,
	loadRetainedBranches func(context.Context) (map[string]struct{}, error),
) (bool, error) {
	releaseLifecycle, err := acquireBranchReconcile(ctx, meta.jobID)
	if err != nil {
		return false, err
	}
	defer releaseLifecycle()
	if _, err := os.Lstat(meta.root); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	jobRoot := filepath.Join(filepath.Dir(meta.root), meta.jobID)
	if info, err := os.Stat(jobRoot); err == nil && info.IsDir() {
		return false, nil
	}
	if enginepaths.WorkerPlacementFolderOf(jobRoot) == "" && branchIsPlaced(stateRoot, jobRoot) {
		return false, nil
	}
	releaseProvision, err := tryWorkspaceProvisionLock(jobRoot)
	if errors.Is(err, ErrBranchInUse) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	latestRetained, err := loadRetainedBranches(ctx)
	if err != nil {
		releaseProvision()
		return false, err
	}
	if _, keep := latestRetained[meta.jobID]; keep {
		releaseProvision()
		return false, nil
	}
	if info, err := os.Stat(jobRoot); err == nil && info.IsDir() {
		releaseProvision()
		return false, nil
	}
	if err := os.RemoveAll(meta.root); err != nil {
		releaseProvision()
		return false, fmt.Errorf("remove orphan sandbox meta %s: %w", meta.root, err)
	}
	releaseProvision()
	removeBranchLocks(jobRoot)
	if err := forgetPlacedBranch(ctx, stateRoot, jobRoot); err != nil {
		return false, err
	}
	slog.InfoContext(ctx, "removed orphan worker sandbox meta",
		"project_dir", strings.TrimSpace(primaryDir),
		"job_id", meta.jobID,
		"path", meta.root,
	)
	return true, nil
}

func removeJobSandboxTreeAndMeta(root string) error {
	root = strings.TrimSpace(root)
	if root == "" {
		return fmt.Errorf("sandbox root required")
	}
	if err := RefuseMountedBranch(root); err != nil {
		return err
	}
	meta := enginepaths.MetaDirForBranchRoot(root)
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("remove stale sandbox %s: %w", root, err)
	}
	if err := os.RemoveAll(meta); err != nil {
		return fmt.Errorf("remove stale sandbox meta %s: %w", meta, err)
	}
	return nil
}

func sandboxDirModTime(root string) (time.Time, error) {
	info, err := adapter.BoundedLstat(root)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}
