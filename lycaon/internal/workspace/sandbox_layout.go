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
	"github.com/lycaon/lycaon/internal/workerplacement"
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
	keyPath, err := branchKeyPath(layout, activeRootID)
	if err != nil {
		return nil, SandboxLayout{}, err
	}
	logicalRoot := m.jobRoot(keyPath, jobID)
	release, err := acquireWorkspaceProvisionLock(ctx, logicalRoot)
	if err != nil {
		return nil, SandboxLayout{}, err
	}
	defer release()
	if existing, err := m.completeBranch(logicalRoot, layout); err != nil {
		return nil, SandboxLayout{}, err
	} else if existing != "" {
		return &Binding{ID: jobID, Root: existing}, layout, nil
	}
	decision, err := m.decidePlacement(ctx, layout.Roots, sources.Roots)
	if err != nil {
		return nil, SandboxLayout{}, classifyProvisionError(err)
	}
	branchRoot := enginepaths.JobBranchDir(decision.Base, keyPath, jobID)
	if err := workerplacement.AssertOutsideRoots(branchRoot, placementRoots(layout.Roots, sources.Roots)); err != nil {
		return nil, SandboxLayout{}, err
	}
	if branchRoot != logicalRoot {
		releasePlaced, err := acquireWorkspaceProvisionLock(ctx, branchRoot)
		if err != nil {
			return nil, SandboxLayout{}, err
		}
		defer releasePlaced()
	}
	binding, err := m.createPlacedSkeleton(ctx, logicalRoot, branchRoot, layout, jobID, decision)
	if err != nil {
		return nil, SandboxLayout{}, err
	}
	preparation, err := m.snapshotSourceRoots(ctx, binding.Root, sources.Roots, decision)
	if err != nil {
		_ = m.DestroyWorkerWorkspace(binding)
		return nil, SandboxLayout{}, err
	}
	binding.Preparation = preparation
	return binding, layout, nil
}

func branchKeyPath(layout SandboxLayout, activeRootID string) (string, error) {
	if len(layout.Roots) == 1 {
		primary, err := projectroot.PrimaryRoot(layout.Roots)
		if err != nil {
			return "", err
		}
		return primary.Path, nil
	}
	active, err := projectroot.ActiveRoot(layout.Roots, activeRootID)
	if err != nil {
		return "", err
	}
	return active.Path, nil
}

const provisionLockSuffix = ".provision.lock"

func tryWorkspaceProvisionLock(branchRoot string) (func(), error) {
	if err := branchVolumeAvailable(branchRoot); err != nil {
		return nil, err
	}
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
	if err := branchVolumeAvailable(branchRoot); err != nil {
		return nil, err
	}
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

func (m *Manager) snapshotSourceRoots(ctx context.Context, branchRoot string, sources []projectroot.RootRef, decision workerplacement.Decision) (Preparation, error) {
	out := Preparation{Clonable: decision.Clonable, NotClonableReason: decision.Reason}
	if decision.Folder != nil {
		out.PlacementFolder = decision.Folder.Path
	}
	for _, source := range sources {
		target := branchRoot
		if len(sources) > 1 {
			dir, err := projectroot.BranchDirForID(source.ID)
			if err != nil {
				return out, err
			}
			target = filepath.Join(branchRoot, dir)
		}
		prepared, err := m.provisionRoot(ctx, source.Path, target, decision)
		if err != nil {
			return out, err
		}
		out.Roots = append(out.Roots, prepared)
	}
	return out, markSnapshotComplete(enginepaths.MetaDirForBranchRoot(branchRoot))
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
