package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/workerplacement"
)

// BuildLocalDataInventory aggregates workspace job trees and coordinator recovery records into the
// three local-data buckets: BucketRetainedWorkerResults, BucketInterruptedWorkingData, and
// BucketDisposableRuntimeCache.
func BuildLocalDataInventory(
	ctx context.Context,
	branchRoot string,
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
		if !t.Present {
			continue
		}
		inv.Entries = append(inv.Entries, recovery.InventoryEntry{
			ID:        "branch-" + t.ProjectKey + "-" + t.JobID,
			Bucket:    classifyBranchTree(coord, t.JobID),
			WorkerID:  t.JobID,
			Path:      t.Root,
			Bytes:     t.AllocatedBytes,
			UpdatedAt: t.LastUsed,
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
	// Unavailable marks a recorded branch whose volume is absent. Nothing is
	// known about its tree, and nothing acts on it until the volume returns.
	Unavailable bool
	// AllocatedBytes is the allocated on-disk size of a present tree, not its
	// logical size.
	AllocatedBytes int64
	LastUsed       time.Time
	Roots          []projectroot.RootRef
}

// classifyBranchTree separates a tree a retained checkpoint can rebuild from one that may hold
// the only copy of its work, including when its recovery record cannot be read.
func classifyBranchTree(coord *recovery.Coordinator, jobID string) recovery.InventoryBucket {
	if coord == nil {
		return recovery.BucketInterruptedWorkingData
	}
	journal, err := coord.LoadWorkerJournal(jobID)
	if err == nil && journal.LastPublishedCheckpointID != "" && journal.ActivePhase == "" {
		return recovery.BucketDisposableRuntimeCache
	}
	return recovery.BucketInterruptedWorkingData
}

// ListJobTrees inventories every branch under branchRoot and the registered
// placement folders, across projects.
func ListJobTrees(ctx context.Context, branchRoot string) ([]JobTree, error) {
	branchRoot = strings.TrimSpace(branchRoot)
	if branchRoot == "" {
		return nil, nil
	}
	stateRoot := filepath.Dir(branchRoot)
	byBranch := map[string]JobTree{}
	for _, base := range branchBases(stateRoot, branchRoot) {
		trees, err := listJobTreesUnder(ctx, base)
		if err != nil {
			return nil, err
		}
		for _, tree := range trees {
			mergeJobTree(byBranch, tree, base == branchRoot)
		}
	}
	placed, err := workerplacement.PlacedBranches(stateRoot)
	if err != nil {
		return nil, err
	}
	for _, branch := range placed {
		if !branch.Available {
			mergeJobTree(byBranch, JobTree{ProjectKey: branch.ProjectKey, JobID: branch.JobID, Root: branch.Root, Unavailable: true}, false)
		}
	}
	out := make([]JobTree, 0, len(byBranch))
	for _, tree := range byBranch {
		out = append(out, tree)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProjectKey != out[j].ProjectKey {
			return out[i].ProjectKey < out[j].ProjectKey
		}
		return out[i].JobID < out[j].JobID
	})
	return out, nil
}

// mergeJobTree keeps one row per job. A state-folder tree wins, as it does when
// resolving; otherwise the placed record replaces its state-folder layout record.
func mergeJobTree(byBranch map[string]JobTree, tree JobTree, inState bool) {
	key := workerplacement.BranchKey(tree.ProjectKey, tree.JobID)
	if existing, ok := byBranch[key]; ok && existing.Present && !inState {
		return
	}
	if _, ok := byBranch[key]; ok && inState && !tree.Present {
		return
	}
	byBranch[key] = tree
}

func listJobTreesUnder(ctx context.Context, branchRoot string) ([]JobTree, error) {
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
		if !project.IsDir() || !validProjectKey(project.Name()) {
			continue
		}
		trees, err := listProjectJobTrees(ctx, filepath.Join(branchRoot, project.Name()), project.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, trees...)
	}
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
		// A mount's bytes are not the tree's, and a dead one fails every stat beneath it.
		if t.Present && RefuseMountedBranch(t.Root) == nil {
			bytes, err := treeAllocatedBytes(ctx, t.Root)
			if err != nil {
				return nil, err
			}
			t.AllocatedBytes = bytes
			// A byte-copied base holds as much again; a cloned one shows what it does not share.
			if base := enginepaths.PristineTreeForBranchRoot(t.Root); adapterTreeExists(base) {
				pristine, err := treeAllocatedBytes(ctx, base)
				if err != nil {
					return nil, err
				}
				t.AllocatedBytes += pristine
			}
		}
		out = append(out, *t)
	}
	return out, nil
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

func adapterTreeExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

// validProjectKey recognizes the directories branch storage keeps per project root.
func validProjectKey(key string) bool {
	if len(key) != 16 {
		return false
	}
	for _, char := range key {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}
