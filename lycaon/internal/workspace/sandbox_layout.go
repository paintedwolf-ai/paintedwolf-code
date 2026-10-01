package workspace

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/filelock"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// SandboxLayout describes how a worker branch maps project roots into the sandbox.
type SandboxLayout struct {
	Roots []projectroot.RootRef
}

func validateLayout(layout SandboxLayout) error {
	if len(layout.Roots) == 0 {
		return fmt.Errorf("sandbox layout roots required")
	}
	for i, root := range layout.Roots {
		if root.ID == "" || root.ID != strings.TrimSpace(root.ID) {
			return fmt.Errorf("sandbox layout root %d missing id", i)
		}
		if strings.TrimSpace(root.Path) == "" {
			return fmt.Errorf("sandbox layout root %d missing path", i)
		}
		if len(layout.Roots) > 1 && (root.Label == "" || root.Label != strings.TrimSpace(root.Label)) {
			return fmt.Errorf("sandbox layout root %q missing label", root.ID)
		}
		if len(layout.Roots) > 1 {
			if _, err := projectroot.BranchDirForID(root.ID); err != nil {
				return fmt.Errorf("sandbox layout root %q: %w", root.ID, err)
			}
		}
	}
	return nil
}

// CreateWorkerWorkspaceFromSources snapshots sources under canonical root identities.
func (m *Manager) CreateWorkerWorkspaceFromSources(
	ctx context.Context,
	roots, sourceRoots []projectroot.RootRef,
	activeRootID, jobID string,
) (*Binding, SandboxLayout, error) {
	if len(roots) == 0 {
		return nil, SandboxLayout{}, fmt.Errorf("project roots required")
	}
	if len(sourceRoots) != len(roots) {
		return nil, SandboxLayout{}, fmt.Errorf("workspace source roots do not match project roots")
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		jobID = uuid.NewString()
	}
	layout := SandboxLayout{Roots: append([]projectroot.RootRef(nil), roots...)}
	sources := SandboxLayout{Roots: append([]projectroot.RootRef(nil), sourceRoots...)}
	for i := range layout.Roots {
		abs, err := absDir(layout.Roots[i].Path)
		if err != nil {
			return nil, SandboxLayout{}, err
		}
		layout.Roots[i].Path = abs
		sourceAbs, err := absDir(sources.Roots[i].Path)
		if err != nil {
			return nil, SandboxLayout{}, err
		}
		sources.Roots[i].Path = sourceAbs
		if layout.Roots[i].ID != sources.Roots[i].ID ||
			layout.Roots[i].Label != sources.Roots[i].Label ||
			layout.Roots[i].IsPrimary != sources.Roots[i].IsPrimary {
			return nil, SandboxLayout{}, fmt.Errorf("workspace source root %d identity mismatch", i)
		}
	}
	if err := validateLayout(layout); err != nil {
		return nil, SandboxLayout{}, err
	}
	if err := validateLayout(sources); err != nil {
		return nil, SandboxLayout{}, err
	}
	var branchRoot string
	if len(layout.Roots) == 1 {
		primary, err := projectroot.PrimaryRoot(layout.Roots)
		if err != nil {
			return nil, SandboxLayout{}, err
		}
		branchRoot = m.jobRoot(primary.Path, jobID)
	} else {
		active, err := projectroot.ActiveRoot(layout.Roots, activeRootID)
		if err != nil {
			return nil, SandboxLayout{}, err
		}
		branchRoot = m.jobRoot(active.Path, jobID)
	}
	release, err := acquireWorkspaceProvisionLock(ctx, branchRoot)
	if err != nil {
		return nil, SandboxLayout{}, err
	}
	defer release()
	if completeBranchMatchesLayout(branchRoot, layout) {
		return &Binding{ID: jobID, Root: branchRoot}, layout, nil
	}
	binding, err := m.createBranchSkeleton(ctx, branchRoot, layout, jobID)
	if err != nil {
		return nil, SandboxLayout{}, err
	}
	if err := m.snapshotSourceRoots(ctx, binding.Root, sources.Roots); err != nil {
		_ = m.DestroyWorkerWorkspace(binding)
		return nil, SandboxLayout{}, err
	}
	return binding, layout, nil
}

const provisionLockSuffix = ".provision.lock"

func tryWorkspaceProvisionLock(branchRoot string) (func(), error) {
	file, err := filelock.Open(branchRoot + provisionLockSuffix)
	if err != nil {
		return nil, err
	}
	held, err := filelock.TryExclusive(file)
	if err != nil || !held {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrBranchInUse
	}
	return func() {
		_ = filelock.Unlock(file)
		_ = file.Close()
	}, nil
}

func acquireWorkspaceProvisionLock(ctx context.Context, branchRoot string) (func(), error) {
	file, err := filelock.Open(branchRoot + provisionLockSuffix)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		held, lockErr := filelock.TryExclusive(file)
		if lockErr != nil {
			_ = file.Close()
			return nil, lockErr
		}
		if held {
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

func completeBranchMatchesLayout(branchRoot string, want SandboxLayout) bool {
	got, err := LoadBranchLayout(branchRoot)
	if err != nil || len(got.Roots) != len(want.Roots) {
		return false
	}
	for i := range want.Roots {
		if got.Roots[i].ID != want.Roots[i].ID || got.Roots[i].Label != want.Roots[i].Label ||
			got.Roots[i].IsPrimary != want.Roots[i].IsPrimary ||
			filepath.Clean(got.Roots[i].Path) != filepath.Clean(want.Roots[i].Path) {
			return false
		}
	}
	return true
}

func (m *Manager) snapshotSourceRoots(ctx context.Context, branchRoot string, sources []projectroot.RootRef) error {
	if len(sources) == 1 {
		if err := m.provisionRoot(ctx, sources[0].Path, branchRoot); err != nil {
			return err
		}
	} else {
		for _, source := range sources {
			dir, err := projectroot.BranchDirForID(source.ID)
			if err != nil {
				return err
			}
			if err := m.provisionRoot(ctx, source.Path, filepath.Join(branchRoot, dir)); err != nil {
				return err
			}
		}
	}
	return markSnapshotComplete(enginepaths.MetaDirForBranchRoot(branchRoot))
}

// jobIDForBranch is the branch directory name: the worker job id.
func jobIDForBranch(branchRoot string) string {
	return filepath.Base(filepath.Clean(branchRoot))
}

// BranchRootRefs returns branch paths with canonical root identities.
func BranchRootRefs(branchRoot string) ([]projectroot.RootRef, error) {
	branchRoot, err := cleanBranchRoot(branchRoot)
	if err != nil {
		return nil, err
	}
	layout, err := LoadBranchLayout(branchRoot)
	if err != nil {
		return nil, err
	}
	return branchRootPaths(branchRoot, layout.Roots)
}

// BranchRootAddresses maps recorded topology without requiring a materialized tree.
func BranchRootAddresses(branchRoot string) ([]projectroot.RootRef, error) {
	branchRoot, err := cleanBranchRoot(branchRoot)
	if err != nil {
		return nil, err
	}
	roots, err := LoadBranchRoots(branchRoot)
	if err != nil {
		return nil, err
	}
	return branchRootPaths(branchRoot, roots)
}

func branchRootPaths(branchRoot string, source []projectroot.RootRef) ([]projectroot.RootRef, error) {
	roots := append([]projectroot.RootRef(nil), source...)
	if len(roots) == 1 {
		roots[0].Path = branchRoot
		return roots, nil
	}
	for i := range roots {
		dir, err := projectroot.BranchDirForID(roots[i].ID)
		if err != nil {
			return nil, err
		}
		roots[i].Path = filepath.Join(branchRoot, dir)
	}
	return roots, nil
}
