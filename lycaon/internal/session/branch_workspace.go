package session

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workspace"
)

type branchWorkspace struct {
	branchRoot string
}

var _ tools.BranchWorkspace = (*branchWorkspace)(nil)

func newBranchWorkspace(branchRoot string) (tools.BranchWorkspace, error) {
	branchRoot = strings.TrimSpace(branchRoot)
	if branchRoot == "" || !filepath.IsAbs(branchRoot) {
		return nil, fmt.Errorf("absolute worker branch root required")
	}
	return &branchWorkspace{branchRoot: filepath.Clean(branchRoot)}, nil
}

func (b *branchWorkspace) ValidateMeta(ctx context.Context) error {
	if b == nil {
		return fmt.Errorf("branch workspace not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := workspace.LoadBranchLayout(b.branchRoot)
	return err
}

func (b *branchWorkspace) EnsureParents(ctx context.Context, branchRel string) error {
	if err := b.ValidateMeta(ctx); err != nil {
		return err
	}
	meta, err := workspace.LoadJobMeta(enginepaths.MetaDirForBranchRoot(b.branchRoot))
	if err != nil {
		return err
	}
	branchRel = strings.TrimSpace(branchRel)
	if filepath.IsAbs(filepath.FromSlash(branchRel)) || sandbox.HasParentTraversal(branchRel) {
		return fmt.Errorf("invalid branch path %q", branchRel)
	}
	branchRel = filepath.ToSlash(branchRel)
	if len(meta.Roots) == 1 {
		return workspace.EnsureParents(b.branchRoot, branchRel)
	}
	parts := strings.SplitN(branchRel, "/", 2)
	knownRoot := false
	for _, root := range meta.Roots {
		dir, dirErr := projectroot.BranchDirForID(root.ID)
		if dirErr == nil && dir == parts[0] {
			knownRoot = true
			break
		}
	}
	if !knownRoot {
		return fmt.Errorf("unknown branch root %q", parts[0])
	}
	rootDir := filepath.Join(b.branchRoot, parts[0])
	rel := "."
	if len(parts) == 2 {
		rel = parts[1]
	}
	return workspace.EnsureParents(rootDir, rel)
}
