package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/enginepaths"
)

// ReconcileStaleSandboxes removes snapshotted candidates absent from current task state.
func ReconcileStaleSandboxes(
	ctx context.Context,
	branchRoot, primaryDir string,
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
	for _, dir := range listing.jobs {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if _, keep := retainJobIDs[dir.jobID]; keep {
			continue
		}
		if err := removeJobSandbox(dir.root); err != nil {
			return removed, err
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
		return removed, err
	}
	return removed + orphans, nil
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
