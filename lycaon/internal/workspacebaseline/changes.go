package workspacebaseline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// Changes compares metadata without loading the complete manifest into memory.
func (r *Reader) Changes(ctx context.Context, roots []projectroot.RootRef, branch string) ([]string, error) {
	var changed []string
	err := r.EachChange(ctx, roots, branch, func(path string) error {
		changed = append(changed, path)
		if len(changed) > DefaultMaxOverlayFiles {
			return &OverlayBudgetExceededError{
				Files:    len(changed),
				MaxFiles: DefaultMaxOverlayFiles,
				Reason:   fmt.Sprintf("%d changed files exceeds limit of %d", len(changed), DefaultMaxOverlayFiles),
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(changed)
	return changed, nil
}

// EachChange visits every path that differs from the manifest, whatever their number.
func (r *Reader) EachChange(ctx context.Context, roots []projectroot.RootRef, branch string, visit func(path string) error) error {
	err := r.Each(ctx, func(path string, before File) error {
		abs, err := branchPath(roots, branch, path)
		if err != nil {
			return err
		}
		after, err := os.Lstat(abs)
		// ENOTDIR: a directory above this path is now a file, so the path is gone.
		if err != nil && !os.IsNotExist(err) && !errors.Is(err, syscall.ENOTDIR) {
			return err
		}
		if err != nil || after.IsDir() || after.Size() != before.Size || after.ModTime().UnixNano() != before.MtimeNano {
			if err := visit(path); err != nil {
				return err
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return err
	}
	return Branch(roots, branch)(ctx, func(file CaptureFile) error {
		_, exists, err := r.Lookup(ctx, file.Path)
		if err != nil {
			return err
		}
		if !exists {
			return visit(file.Path)
		}
		return nil
	})
}

// BranchLocation is where one manifest path lives: its project root and the branch tree file.
type BranchLocation struct {
	RootID string
	Rel    string
	Abs    string
}

// LocateInBranch maps a manifest path onto its root and branch tree file.
func LocateInBranch(roots []projectroot.RootRef, branch, path string) (BranchLocation, error) {
	abs, err := branchPath(roots, branch, path)
	if err != nil {
		return BranchLocation{}, err
	}
	if len(roots) > 1 {
		chosen, rel, err := manifestRoot(roots, path)
		if err != nil {
			return BranchLocation{}, err
		}
		return BranchLocation{RootID: chosen.ID, Rel: rel, Abs: abs}, nil
	}
	location := BranchLocation{Rel: path, Abs: abs}
	if len(roots) == 1 {
		location.RootID = roots[0].ID
	}
	return location, nil
}

// manifestRoot resolves a multi-root manifest path to its root and root-relative path.
func manifestRoot(roots []projectroot.RootRef, path string) (projectroot.RootRef, string, error) {
	primary, err := projectroot.PrimaryRoot(roots)
	if err != nil {
		return projectroot.RootRef{}, "", err
	}
	if !strings.HasPrefix(path, "@") {
		return primary, path, nil
	}
	label, rel, ok := strings.Cut(path[1:], "/")
	if !ok {
		return projectroot.RootRef{}, "", fmt.Errorf("invalid qualified baseline path")
	}
	for _, root := range roots {
		if strings.EqualFold(root.Label, label) {
			return root, rel, nil
		}
	}
	return projectroot.RootRef{}, "", fmt.Errorf("unknown baseline root")
}

func branchPath(roots []projectroot.RootRef, branch, path string) (string, error) {
	base := branch
	if len(roots) > 1 {
		chosen, rel, err := manifestRoot(roots, path)
		if err != nil {
			return "", err
		}
		path = rel
		dir, err := projectroot.BranchDirForID(chosen.ID)
		if err != nil {
			return "", err
		}
		base = filepath.Join(branch, dir)
	}
	if path == "" || filepath.IsAbs(path) || sandbox.HasParentTraversal(path) {
		return "", fmt.Errorf("invalid baseline path")
	}
	return filepath.Join(base, filepath.FromSlash(path)), nil
}

// CanonicalPath maps a manifest path onto the project root it was captured
// from, the same way branchPath maps it onto the branch tree.
func CanonicalPath(roots []projectroot.RootRef, path string) (string, error) {
	if len(roots) == 0 {
		return "", fmt.Errorf("project roots required")
	}
	chosen := roots[0]
	if len(roots) > 1 {
		root, rel, err := manifestRoot(roots, path)
		if err != nil {
			return "", err
		}
		chosen, path = root, rel
	}
	if path == "" || filepath.IsAbs(path) || sandbox.HasParentTraversal(path) {
		return "", fmt.Errorf("invalid baseline path")
	}
	return filepath.Join(chosen.Path, filepath.FromSlash(path)), nil
}
