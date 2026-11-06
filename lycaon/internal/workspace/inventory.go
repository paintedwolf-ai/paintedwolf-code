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
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/workerplacement"
)

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
		if !project.IsDir() || !validSeedKey(project.Name()) {
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
