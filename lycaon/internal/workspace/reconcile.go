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

// ReconcileStaleSandboxes removes snapshotted candidates absent from current task state,
// settling each one first. A blocked candidate is kept and reported; the rest proceed.
func ReconcileStaleSandboxes(
	ctx context.Context,
	branchRoot, primaryDir string,
	settle SandboxSettler,
	loadRetainedJobIDs func(context.Context) (map[string]struct{}, error),
) (removed int, err error) {
	listing, err := listSandboxEntries(branchRoot, primaryDir)
	if err != nil {
		return 0, err
	}
	retainJobIDs, err := loadRetainedJobIDs(ctx)
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
		if _, keep := retainJobIDs[dir.jobID]; keep {
			continue
		}
		if err := settleBeforeRemoval(ctx, settle, dir.jobID, dir.root); err != nil {
			blocked = append(blocked, err)
			continue
		}
		if err := removeJobSandbox(dir.root); err != nil {
			return removed, errors.Join(append(blocked, err)...)
		}
		removed++
		slog.InfoContext(ctx, "removed stale worker sandbox",
			"project_dir", strings.TrimSpace(primaryDir),
			"job_id", dir.jobID,
			"path", dir.root,
		)
	}
	orphans, err := removeUnretainedOrphanMeta(ctx, primaryDir, listing.metas, retainJobIDs)
	if err != nil {
		return removed, errors.Join(append(blocked, err)...)
	}
	return removed + orphans, errors.Join(blocked...)
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

func removeUnretainedOrphanMeta(ctx context.Context, primaryDir string, metas []sandboxDir, retainJobIDs map[string]struct{}) (int, error) {
	removed := 0
	for _, meta := range metas {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if _, keep := retainJobIDs[meta.jobID]; keep {
			continue
		}
		jobRoot := filepath.Join(filepath.Dir(meta.root), meta.jobID)
		if info, err := os.Stat(jobRoot); err == nil && info.IsDir() {
			continue
		}
		if err := os.RemoveAll(meta.root); err != nil {
			return removed, fmt.Errorf("remove orphan sandbox meta %s: %w", meta.root, err)
		}
		removed++
		slog.InfoContext(ctx, "removed orphan worker sandbox meta",
			"project_dir", strings.TrimSpace(primaryDir),
			"job_id", meta.jobID,
			"path", meta.root,
		)
	}
	return removed, nil
}

func removeJobSandbox(root string) error {
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
	removeBranchLocks(root)
	return nil
}

func sandboxDirModTime(root string) (time.Time, error) {
	info, err := os.Stat(root)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}
