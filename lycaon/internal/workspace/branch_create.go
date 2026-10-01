package workspace

import (
	"context"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/projectroot"
)

func (m *Manager) createBranchSkeleton(
	ctx context.Context,
	branchRoot string,
	layout SandboxLayout,
	jobID string,
) (*Binding, error) {
	if err := os.MkdirAll(filepath.Dir(branchRoot), 0o700); err != nil {
		return nil, classifyProvisionError(err)
	}
	metaDir := enginepaths.MetaDirForBranchRoot(branchRoot)
	_ = os.RemoveAll(metaDir)
	if err := os.RemoveAll(branchRoot); err != nil {
		return nil, classifyProvisionError(err)
	}
	if err := os.MkdirAll(branchRoot, 0o750); err != nil {
		return nil, classifyProvisionError(err)
	}
	if len(layout.Roots) > 1 {
		for _, root := range layout.Roots {
			dir, err := projectroot.BranchDirForID(root.ID)
			if err != nil {
				_ = os.RemoveAll(branchRoot)
				return nil, err
			}
			if err := os.MkdirAll(filepath.Join(branchRoot, dir), 0o750); err != nil {
				_ = os.RemoveAll(branchRoot)
				return nil, classifyProvisionError(err)
			}
		}
	}
	if err := WriteJobMeta(metaDir, jobMetaFromLayout(layout)); err != nil {
		_ = os.RemoveAll(branchRoot)
		_ = os.RemoveAll(metaDir)
		return nil, err
	}
	reportPreparation(ctx, PreparationProgress{Stage: "branch_created"})
	return &Binding{ID: jobID, Root: branchRoot}, nil
}
