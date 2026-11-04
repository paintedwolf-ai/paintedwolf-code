package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
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

// CreateWorkerWorkspaceFromSources snapshots sources under canonical root identities.
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
		_ = m.DestroyWorkerWorkspace(binding)
		return nil, SandboxLayout{}, err
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

func snapshotSourceRoots(ctx context.Context, m *Manager, binding *Binding, sources []projectroot.RootRef) error {
	branchRoot := binding.Root
	services, err := m.layered()
	if err != nil {
		return err
	}
	choice, err := SelectWorkspaceAdapter(ctx, adapter.ProbeConfig{RuntimeDir: filepath.Join(m.branchRoot, adapterRuntimeDirName)}, m.selector)
	if err != nil {
		return classifyProvisionError(err)
	}
	binding.Selection = choice
	binding.Adapter = choice.Adapter
	_, hints, _ := workspaceSelectionFromContext(ctx)

	if choice.Facts.PreparationKind == adapter.PreparationMounted {
		view, cv, err := prepareMountedWorkspaceView(ctx, m, binding, sources, choice)
		if err != nil {
			return classifyProvisionError(err)
		}
		binding.View = view
		m.recordBranchViews(branchRoot, choice, view)
		return markSnapshotCompleteWithClaim(branchRoot, choice, view, cv, hints)
	}

	if len(sources) == 1 {
		rtDir := filepath.Join(m.branchRoot, adapterRuntimeDirName, enginepaths.ProjectKey(sources[0].Path))
		view, err := provisionRoot(ctx, m, sources[0], branchRoot, rtDir, binding.ID, choice.Facts.FallbackReason)
		if err != nil {
			return err
		}
		binding.View = view
		m.recordBranchViews(branchRoot, choice, view)
		if err := adoptMaterializedGit(ctx, services, binding.ID, binding.ID, "", sources[0], view, branchRoot); err != nil {
			return err
		}
		return markSnapshotCompleteWithClaim(branchRoot, choice, view, nil, hints)
	}
	var views []*adapter.PreparedView
	for _, source := range sources {
		dir, err := projectroot.BranchDirForID(source.ID)
		if err != nil {
			return err
		}
		rtDir := filepath.Join(m.branchRoot, adapterRuntimeDirName, enginepaths.ProjectKey(source.Path))
		viewID := binding.ID + "-" + source.ID
		view, err := provisionRoot(ctx, m, source, filepath.Join(branchRoot, dir), rtDir, viewID, choice.Facts.FallbackReason)
		if err != nil {
			return err
		}
		views = append(views, view)
		m.recordBranchViews(branchRoot, choice, views...)
		workerID, branchDir := viewID, ""
		if source.IsPrimary {
			workerID, branchDir = binding.ID, branchRoot
		}
		if err := adoptMaterializedGit(ctx, services, viewID, workerID, branchDir, source, view, filepath.Join(branchRoot, dir)); err != nil {
			return err
		}
		if source.IsPrimary || binding.View == nil {
			binding.View = view
		}
	}
	return markSnapshotCompleteWithClaim(branchRoot, choice, binding.View, nil, hints)
}

// adoptMaterializedGit hands the repositories a seed prepared to the private Git manager. Only the
// primary root's view carries the worker's identity, which is what Git admission looks a view up by.
func adoptMaterializedGit(
	ctx context.Context,
	services *layeredServices,
	viewID, workerID, branchDir string,
	source projectroot.RootRef,
	view *adapter.PreparedView,
	worktreeDir string,
) error {
	repos := make([]privategit.SeededRepo, 0, len(view.SeededGitRepos))
	for key, seeded := range view.SeededGitRepos {
		_, repoRel, _ := strings.Cut(key, ":")
		repos = append(repos, privategit.SeededRepo{
			RepoRel: repoRel, PrivateGitDir: seeded.PrivateMetaDir,
			Metrics: privategit.InitMetrics{ObjectsReused: seeded.ObjectsReused, BytesImported: seeded.BytesImported, BytesReused: seeded.BytesReused},
		})
	}
	err := services.privateGit.AdoptMaterializedView(ctx, privategit.MaterializedViewBinding{
		ViewID: viewID, WorkerID: workerID, RootID: view.PrimaryRootID,
		TrunkDir: source.Path, WorktreeDir: worktreeDir, Core: view.Core, Repos: repos,
	})
	if err != nil {
		return err
	}
	if branchDir == "" {
		return nil
	}
	return services.privateGit.BindViewBranch(viewID, branchDir)
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
