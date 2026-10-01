package workspacebaseline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// Changes compares metadata without loading the complete manifest into memory.
func (r *Reader) Changes(ctx context.Context, roots []projectroot.RootRef, branch string) ([]string, error) {
	var changed []string
	err := r.Each(ctx, func(path string, before File) error {
		abs, err := branchPath(roots, branch, path)
		if err != nil {
			return err
		}
		after, err := os.Lstat(abs)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err != nil || after.IsDir() || after.Size() != before.Size || after.ModTime().UnixNano() != before.MtimeNano {
			changed = append(changed, path)
			if len(changed) > DefaultMaxOverlayFiles {
				return &OverlayBudgetExceededError{
					Files:    len(changed),
					MaxFiles: DefaultMaxOverlayFiles,
					Reason:   fmt.Sprintf("%d changed files exceeds limit of %d", len(changed), DefaultMaxOverlayFiles),
				}
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}

	err = Branch(roots, branch)(ctx, func(file CaptureFile) error {
		_, exists, err := r.Lookup(ctx, file.Path)
		if err != nil {
			return err
		}
		if !exists {
			changed = append(changed, file.Path)
			if len(changed) > DefaultMaxOverlayFiles {
				return &OverlayBudgetExceededError{
					Files:    len(changed),
					MaxFiles: DefaultMaxOverlayFiles,
					Reason:   fmt.Sprintf("%d changed files exceeds limit of %d", len(changed), DefaultMaxOverlayFiles),
				}
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

func branchPath(roots []projectroot.RootRef, branch, path string) (string, error) {
	base := branch
	if len(roots) > 1 {
		primary, err := projectroot.PrimaryRoot(roots)
		if err != nil {
			return "", err
		}
		chosen := primary
		if strings.HasPrefix(path, "@") {
			label, rel, ok := strings.Cut(path[1:], "/")
			if !ok {
				return "", fmt.Errorf("invalid qualified baseline path")
			}
			found := false
			for _, root := range roots {
				if strings.EqualFold(root.Label, label) {
					chosen = root
					found = true
					break
				}
			}
			if !found {
				return "", fmt.Errorf("unknown baseline root")
			}
			path = rel
		}
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
		primary, err := projectroot.PrimaryRoot(roots)
		if err != nil {
			return "", err
		}
		chosen = primary
		if strings.HasPrefix(path, "@") {
			label, rel, ok := strings.Cut(path[1:], "/")
			if !ok {
				return "", fmt.Errorf("invalid qualified baseline path")
			}
			found := false
			for _, root := range roots {
				if strings.EqualFold(root.Label, label) {
					chosen = root
					found = true
					break
				}
			}
			if !found {
				return "", fmt.Errorf("unknown baseline root")
			}
			path = rel
		}
	}
	if path == "" || filepath.IsAbs(path) || sandbox.HasParentTraversal(path) {
		return "", fmt.Errorf("invalid baseline path")
	}
	return filepath.Join(chosen.Path, filepath.FromSlash(path)), nil
}
