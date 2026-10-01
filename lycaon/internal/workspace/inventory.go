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
)

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
