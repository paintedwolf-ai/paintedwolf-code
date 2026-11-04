package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/claim"
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

// CreateWorkerWorkspaceSelected narrows attached roots and subroots using an explicit WorkspaceSelection
// (keeping informational taskPathHints separate) before provisioning the worker workspace (#1658).
func (m *Manager) CreateWorkerWorkspaceSelected(
	ctx context.Context,
	roots, sourceRoots []projectroot.RootRef,
	selection adapter.WorkspaceSelection,
	taskPathHints []string,
	activeRootID, jobID string,
) (*Binding, SandboxLayout, error) {
	ctx = WithWorkspaceSelection(ctx, selection, taskPathHints...)
	return m.CreateWorkerWorkspaceFromSources(ctx, roots, sourceRoots, activeRootID, jobID)
}

func applyWorkspaceSelectionToLayout(
	layout, sources SandboxLayout,
	selection adapter.WorkspaceSelection,
	taskPathHints []string,
	activeRootID string,
) (SandboxLayout, SandboxLayout, string, error) {
	narrowedGrants, narrowedSources, err := ResolveDispatchWorkspaceSelection(sources.Roots, selection, taskPathHints)
	if err != nil {
		return SandboxLayout{}, SandboxLayout{}, "", err
	}
	layoutByID := make(map[string]projectroot.RootRef, len(layout.Roots))
	for _, r := range layout.Roots {
		layoutByID[r.ID] = r
	}
	narrowedLayout := make([]projectroot.RootRef, 0, len(narrowedGrants))
	activeFound := false
	primaryID := ""
	for i, g := range narrowedGrants {
		lr := layoutByID[g.RootID]
		lr.IsPrimary = g.IsPrimary
		if g.Subroot != "" {
			lr.Path = filepath.Join(lr.Path, filepath.FromSlash(g.Subroot))
		}
		narrowedLayout = append(narrowedLayout, lr)
		if lr.ID == activeRootID {
			activeFound = true
		}
		if narrowedSources[i].IsPrimary {
			primaryID = narrowedSources[i].ID
		}
	}
	if !activeFound && primaryID != "" {
		activeRootID = primaryID
	}
	return SandboxLayout{Roots: narrowedLayout}, SandboxLayout{Roots: narrowedSources}, activeRootID, nil
}

// CreateWorkerWorkspaceFromSources prepares a view of sources under canonical root identities.
func (m *Manager) CreateWorkerWorkspaceFromSources(
	ctx context.Context,
	roots, sourceRoots []projectroot.RootRef,
	activeRootID, jobID string,
) (*Binding, SandboxLayout, error) {
	return createWorkerWorkspaceFromSources(ctx, m, roots, sourceRoots, activeRootID, jobID)
}

func createWorkerWorkspaceFromSources(
	ctx context.Context,
	m *Manager,
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
	if sel, hints, ok := workspaceSelectionFromContext(ctx); ok {
		var selErr error
		layout, sources, activeRootID, selErr = applyWorkspaceSelectionToLayout(layout, sources, sel, hints, activeRootID)
		if selErr != nil {
			return nil, SandboxLayout{}, selErr
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
		branchRoot = jobRoot(m, primary.Path, jobID)
	} else {
		active, err := projectroot.ActiveRoot(layout.Roots, activeRootID)
		if err != nil {
			return nil, SandboxLayout{}, err
		}
		branchRoot = jobRoot(m, active.Path, jobID)
	}
	release, err := acquireWorkspaceProvisionLock(ctx, branchRoot)
	if err != nil {
		return nil, SandboxLayout{}, err
	}
	defer release()
	if completeBranchMatchesLayout(branchRoot, layout) {
		b := &Binding{ID: jobID, Root: branchRoot}
		if sel, view, ok := m.lookupBranchViews(branchRoot); ok {
			b.Selection = sel
			b.View = view
			b.Adapter = sel.Adapter
		}
		return b, layout, nil
	}
	binding, err := m.createBranchSkeleton(ctx, branchRoot, layout, jobID)
	if err != nil {
		return nil, SandboxLayout{}, err
	}
	if err := snapshotSourceRoots(ctx, m, binding, sources.Roots); err != nil {
		return nil, SandboxLayout{}, errors.Join(err, m.DestroyWorkerWorkspace(binding))
	}
	return binding, layout, nil
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

// snapshotSourceRoots gives a worker its one provider. A copy that passes its budget has cleaned up
// after itself, so the rule runs again knowing that and moves on to the mount.
func snapshotSourceRoots(ctx context.Context, m *Manager, binding *Binding, sources []projectroot.RootRef) error {
	if _, err := m.layered(); err != nil {
		return err
	}
	grants := grantsForSources(sources)
	projectID := workspaceProjectFromContext(ctx, "")
	_, hints, _ := workspaceSelectionFromContext(ctx)
	var overrun *adapter.BudgetOverrunError
	for {
		reportPreparation(ctx, PreparationProgress{Stage: "probing"})
		choice, err := m.chooseWorkspaceAdapter(ctx, binding.Root, grants, projectID, overrun)
		if err != nil {
			return classifyProvisionError(err)
		}
		binding.Selection, binding.Adapter = choice, choice.Adapter

		var view *adapter.PreparedView
		var cv *claim.ClaimedView
		if choice.Facts.PreparationKind == adapter.PreparationMounted {
			view, cv, err = prepareMountedWorkspaceView(ctx, m, binding, sources, choice)
		} else {
			view, cv, err = m.prepareMaterializedWorkspaceView(ctx, binding, sources, choice)
		}
		if errors.As(err, &overrun) && choice.CopyBudget != nil {
			if err := restoreBranchDirectories(binding, sources); err != nil {
				return classifyProvisionError(err)
			}
			continue
		}
		if err != nil {
			return classifyProvisionError(err)
		}
		binding.View = view
		m.recordBranchViews(binding.Root, choice, view)
		return markSnapshotCompleteWithClaim(binding.Root, choice, view, cv, hints)
	}
}

// restoreBranchDirectories puts back the empty directories a cleaned-up copy took with it.
func restoreBranchDirectories(binding *Binding, sources []projectroot.RootRef) error {
	if err := os.MkdirAll(binding.Root, 0o750); err != nil {
		return err
	}
	if len(sources) == 1 {
		return nil
	}
	for _, source := range sources {
		dir, err := projectroot.BranchDirForID(source.ID)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(binding.Root, dir), 0o750); err != nil {
			return err
		}
	}
	return nil
}

// jobIDForBranch is the branch directory name: the worker job id.
func jobIDForBranch(branchRoot string) string { return filepath.Base(filepath.Clean(branchRoot)) }

// BranchRootRefs returns branch paths with canonical root identities.
func BranchRootRefs(branchRoot string) ([]projectroot.RootRef, error) {
	if clean, err := cleanBranchRoot(branchRoot); err != nil {
		return nil, err
	} else if layout, err := LoadBranchLayout(clean); err != nil {
		return nil, err
	} else {
		return branchRootPaths(clean, layout.Roots)
	}
}

// BranchRootAddresses maps recorded topology without requiring a materialized tree.
func BranchRootAddresses(branchRoot string) ([]projectroot.RootRef, error) {
	if clean, err := cleanBranchRoot(branchRoot); err != nil {
		return nil, err
	} else if roots, err := LoadBranchRoots(clean); err != nil {
		return nil, err
	} else {
		return branchRootPaths(clean, roots)
	}
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
