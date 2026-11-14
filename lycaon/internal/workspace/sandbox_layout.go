package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/claim"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
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
		if root.Label == "" || root.Label != strings.TrimSpace(root.Label) {
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
// (keeping informational taskPathHints separate) before provisioning the worker workspace.
func (m *Manager) CreateWorkerWorkspaceSelected(
	ctx context.Context,
	roots []projectroot.RootRef,
	selection adapter.WorkspaceSelection,
	taskPathHints []string,
	activeRootID, jobID string,
) (*Binding, SandboxLayout, error) {
	ctx = WithWorkspaceSelection(ctx, selection, taskPathHints...)
	return m.CreateWorkerBranch(ctx, roots, activeRootID, jobID)
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

// CreateWorkerBranch creates a worker's branch over live project roots.
func (m *Manager) CreateWorkerBranch(ctx context.Context, roots []projectroot.RootRef, activeRootID, jobID string) (*Binding, SandboxLayout, error) {
	return createWorkerWorkspaceFromSources(ctx, m, roots, roots, activeRootID, jobID, "")
}

// CreateWorkerWorkspaceFromSources prepares a view of sources under canonical root identities.
func (m *Manager) CreateWorkerWorkspaceFromSources(
	ctx context.Context,
	roots, sourceRoots []projectroot.RootRef,
	activeRootID, jobID string,
) (*Binding, SandboxLayout, error) {
	return createWorkerWorkspaceFromSources(ctx, m, roots, sourceRoots, activeRootID, jobID, "")
}

// CreateStackedWorkerBranch creates a worker's branch over live project roots and a parent's immutable
// checkpoint. Nothing reads the parent's working tree.
func (m *Manager) CreateStackedWorkerBranch(ctx context.Context, roots []projectroot.RootRef, activeRootID, jobID, parentCheckpointID string) (*Binding, SandboxLayout, error) {
	if strings.TrimSpace(parentCheckpointID) == "" {
		return nil, SandboxLayout{}, fmt.Errorf("stacked worker %q names no parent checkpoint", jobID)
	}
	return createWorkerWorkspaceFromSources(ctx, m, roots, roots, activeRootID, jobID, parentCheckpointID)
}

func createWorkerWorkspaceFromSources(
	ctx context.Context,
	m *Manager,
	roots, sourceRoots []projectroot.RootRef,
	activeRootID, jobID, parentCheckpointID string,
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
		b := &Binding{ID: jobID, Root: existing}
		if sel, view, ok := m.lookupBranchViews(existing); ok {
			b.Selection, b.View, b.Adapter = sel, view, sel.Adapter
		}
		return b, layout, nil
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
	binding.Preparation = placementPreparation(decision)
	if err := snapshotSourceRoots(ctx, m, binding, sources.Roots, parentCheckpointID, decision); err != nil {
		return nil, SandboxLayout{}, errors.Join(err, m.DestroyWorkerWorkspace(ctx, binding))
	}
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
func snapshotSourceRoots(ctx context.Context, m *Manager, binding *Binding, sources []projectroot.RootRef, parentCheckpointID string, decision workerplacement.Decision) error {
	services, err := m.layered() //nolint:contextcheck // The shared private Git store opens once for the engine, not for this call.
	if err != nil {
		return err
	}
	grants := grantsForSources(sources)
	projectID := workspaceProjectFromContext(ctx, "")
	_, hints, _ := workspaceSelectionFromContext(ctx)
	var overrun *adapter.BudgetOverrunError
	for {
		reportPreparation(ctx, PreparationProgress{Stage: "probing"})
		choice, err := m.chooseWorkspaceAdapter(ctx, binding.Root, grants, projectID, decision, overrun, false)
		if err != nil {
			return classifyProvisionError(err)
		}
		binding.Selection, binding.Adapter = choice, choice.Adapter
		if choice.Facts.PreparationKind == adapter.PreparationMaterialized {
			reportPreparation(ctx, PreparationProgress{
				Strategy: preparationStrategy(choice.Selection), Stage: StageCostDeclared,
				TotalEntries: choice.Selection.DeclaredCost.Entries, TotalBytes: choice.Selection.DeclaredCost.UncloneableBytes,
				NotClonableReason: decision.Reason,
			})
		}

		var view *adapter.PreparedView
		var cv *claim.ClaimedView
		prepare := func(ctx context.Context, parents recovery.StackedParents) (*adapter.PreparedView, error) {
			var prepErr error
			if choice.Facts.PreparationKind == adapter.PreparationMounted {
				view, cv, prepErr = prepareMountedWorkspaceView(ctx, m, binding, sources, choice, parents)
			} else {
				view, cv, prepErr = m.prepareMaterializedWorkspaceView(ctx, binding, sources, choice, parents)
			}
			return view, prepErr
		}
		if parentCheckpointID == "" {
			_, err = prepare(ctx, recovery.StackedParents{})
		} else {
			_, err = services.recovery.PrepareStackedWorkerView(ctx, recovery.StackedViewRequest{
				ChildWorkerID: binding.ID, ChildViewID: binding.ID, ProjectID: projectID,
				ParentCheckpointID: parentCheckpointID, AdapterID: choice.Adapter.ID(),
				Prepare: prepare,
				// The caller destroys a branch whose creation failed, which settles its views.
				Release: func(context.Context, *adapter.PreparedView) error { return nil },
			})
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
		binding.Preparation.Materialized = view.Materialize
		reportPreparation(ctx, PreparationProgress{
			Strategy: preparationStrategy(choice.Selection), Stage: StageBranchPrepared,
			UnreadableEntries: view.Materialize.UnreadableEntries, NotClonableReason: decision.Reason,
		})
		m.recordBranchViews(binding.Root, choice, view)
		err = markSnapshotCompleteWithClaim(binding.Root, choice, view, cv, hints)
		parkIdleView(view)
		return err
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

// placementPreparation is what the placement decision contributes to a binding's preparation facts.
func placementPreparation(decision workerplacement.Decision) Preparation {
	out := Preparation{Clonable: decision.Clonable, NotClonableReason: decision.Reason}
	if decision.Folder != nil {
		out.PlacementFolder = decision.Folder.Path
	}
	return out
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
