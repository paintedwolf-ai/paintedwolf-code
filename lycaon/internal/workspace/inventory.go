package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/filelock"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// BuildLocalDataInventory aggregates workspace job trees, rebuildable seed caches, and
// coordinator recovery records into the three local-data buckets (#1666):
// BucketRetainedWorkerResults, BucketInterruptedWorkingData, and BucketDisposableRuntimeCache.
func BuildLocalDataInventory(
	ctx context.Context,
	branchRoot, seedRoot string,
	coord *recovery.Coordinator,
) (recovery.LocalDataInventory, error) {
	inv := recovery.LocalDataInventory{
		FormatVersion: recovery.FormatVersion,
		UpdatedAt:     time.Now().UTC(),
	}
	if coord != nil {
		loaded, err := coord.LoadInventory()
		if err != nil {
			return recovery.LocalDataInventory{}, err
		}
		inv.Entries = append(inv.Entries, loaded.Entries...)
	}
	trees, err := ListJobTrees(ctx, branchRoot)
	if err != nil {
		return recovery.LocalDataInventory{}, err
	}
	for _, t := range trees {
		bucket := recovery.BucketDisposableRuntimeCache
		if !t.Present {
			bucket = recovery.BucketRetainedWorkerResults
		}
		inv.Entries = append(inv.Entries, recovery.InventoryEntry{
			ID:        "branch-" + t.ProjectKey + "-" + t.JobID,
			Bucket:    bucket,
			WorkerID:  t.JobID,
			Path:      t.Root,
			Bytes:     t.AllocatedBytes,
			UpdatedAt: t.LastUsed,
		})
	}
	seeds, err := ListSeedCaches(ctx, seedRoot)
	if err != nil {
		return recovery.LocalDataInventory{}, err
	}
	for _, s := range seeds {
		inv.Entries = append(inv.Entries, recovery.InventoryEntry{
			ID:        "seed-" + s.ID,
			Bucket:    recovery.BucketDisposableRuntimeCache,
			Path:      s.SourceRoot,
			Bytes:     s.AllocatedBytes,
			UpdatedAt: s.LastUsed,
		})
	}
	return inv, nil
}

// JobTree is one worker branch under the engine's branch root, present on
// disk or evicted down to its .meta record.
type JobTree struct {
	ProjectKey string
	JobID      string
	Root       string
	Present    bool
	// AllocatedBytes is the allocated on-disk size of a present tree, not its
	// logical size.
	AllocatedBytes int64
	LastUsed       time.Time
	Roots          []projectroot.RootRef
}

// SeedCacheStatus describes one retained, rebuildable bridge cache.
type SeedCacheStatus struct {
	ID             string
	SourceRoot     string
	LogicalBytes   int64
	AllocatedBytes int64
	LastUsed       time.Time
}

// ListJobTrees inventories every branch under branchRoot across projects.
func ListJobTrees(ctx context.Context, branchRoot string) ([]JobTree, error) {
	branchRoot = strings.TrimSpace(branchRoot)
	if branchRoot == "" {
		return nil, nil
	}
	projects, err := os.ReadDir(branchRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []JobTree
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !project.IsDir() || !validSeedKey(project.Name()) {
			continue
		}
		trees, err := listProjectJobTrees(ctx, filepath.Join(branchRoot, project.Name()), project.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, trees...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProjectKey != out[j].ProjectKey {
			return out[i].ProjectKey < out[j].ProjectKey
		}
		return out[i].JobID < out[j].JobID
	})
	return out, nil
}

func listProjectJobTrees(ctx context.Context, parent, projectKey string) ([]JobTree, error) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil, err
	}
	byJob := map[string]*JobTree{}
	tree := func(jobID string) *JobTree {
		t, ok := byJob[jobID]
		if !ok {
			t = &JobTree{ProjectKey: projectKey, JobID: jobID, Root: filepath.Join(parent, jobID)}
			byJob[jobID] = t
		}
		return t
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if enginepaths.IsJobMetaDirName(name) {
			if jobID := strings.TrimSuffix(name, enginepaths.JobMetaDirSuffix); jobID != "" {
				tree(jobID)
			}
			continue
		}
		tree(name).Present = true
	}
	out := make([]JobTree, 0, len(byJob))
	for _, t := range byJob {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		t.LastUsed = BranchLastUsed(t.Root)
		if meta, err := LoadJobMeta(enginepaths.MetaDirForBranchRoot(t.Root)); err == nil {
			if layout, err := layoutFromJobMeta(meta); err == nil {
				t.Roots = layout.Roots
			}
		}
		if t.Present {
			bytes, err := treeAllocatedBytes(ctx, t.Root)
			if err != nil {
				return nil, err
			}
			t.AllocatedBytes = bytes
		}
		out = append(out, *t)
	}
	return out, nil
}

// ListSeedCaches inventories valid published bridge caches.
func ListSeedCaches(ctx context.Context, seedRoot string) ([]SeedCacheStatus, error) {
	if strings.TrimSpace(seedRoot) == "" {
		return nil, nil
	}
	candidates, err := seedEvictionCandidates(seedRoot, "")
	if err != nil {
		return nil, err
	}
	statuses := make([]SeedCacheStatus, 0, len(candidates))
	for _, candidate := range candidates {
		_, tree, manifest := loadCurrentSeed(candidate.path)
		if tree == "" || strings.TrimSpace(manifest.SourceRoot) == "" {
			continue
		}
		allocated, err := treeAllocatedBytes(ctx, candidate.path)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, SeedCacheStatus{
			ID:             candidate.key,
			SourceRoot:     manifest.SourceRoot,
			LogicalBytes:   regularBytes(manifest),
			AllocatedBytes: allocated,
			LastUsed:       candidate.lastUsed,
		})
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].LastUsed.After(statuses[j].LastUsed) })
	return statuses, nil
}

// RemoveSeedByID removes one rebuildable bridge cache under an exclusive lease.
func RemoveSeedByID(ctx context.Context, seedRoot, id string) error {
	if strings.TrimSpace(seedRoot) == "" {
		return fmt.Errorf("worker cache storage is unavailable")
	}
	if !validSeedKey(id) {
		return fmt.Errorf("invalid worker cache id")
	}
	release, err := lockSeedAcrossProcesses(ctx, seedRoot, id, true)
	if err != nil {
		return err
	}
	defer release()
	return os.RemoveAll(filepath.Join(seedRoot, id))
}

// RemoveSeed removes only rebuildable host state for sourceRoot.
func RemoveSeed(ctx context.Context, seedRoot, sourceRoot string) error {
	if strings.TrimSpace(seedRoot) == "" || strings.TrimSpace(sourceRoot) == "" {
		return nil
	}
	target := enginepaths.ProjectSeedDir(seedRoot, sourceRoot)
	rel, err := filepath.Rel(filepath.Clean(seedRoot), filepath.Clean(target))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("refusing worker seed removal outside seed root")
	}
	if _, err := os.Stat(target); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	release, err := lockSeedAcrossProcesses(ctx, seedRoot, enginepaths.ProjectKey(sourceRoot), true)
	if err != nil {
		return err
	}
	defer release()
	if err := os.RemoveAll(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func treeAllocatedBytes(ctx context.Context, root string) (int64, error) {
	var total int64
	err := sandbox.SurveyWalk(ctx, root, sandbox.SurveyOptions{IncludeHidden: true},
		func(entry sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
			if entry.IsDir {
				return sandbox.SurveyContinue, nil
			}
			info, err := entry.DirEntry.Info()
			if err != nil {
				return sandbox.SurveyContinue, err
			}
			allocation := fspath.EntryAllocation(info)
			bytes := info.Size()
			if allocation.Known {
				bytes = allocation.Bytes
			}
			total += max(bytes, 0)
			return sandbox.SurveyContinue, nil
		})
	return total, err
}

func lockSeedAcrossProcesses(ctx context.Context, seedRoot, key string, exclusive bool) (func(), error) {
	path := filepath.Join(seedRoot, seedLocksDir, key+".lock")
	file, err := filelock.Open(path)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var locked bool
		if exclusive {
			locked, err = filelock.TryExclusive(file)
		} else {
			locked, err = filelock.TryShared(file)
		}
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		if locked {
			return func() {
				_ = filelock.Unlock(file)
				_ = file.Close()
			}, nil
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
