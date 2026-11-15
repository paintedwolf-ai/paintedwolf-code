package orchestration

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkspaceBinder creates and destroys per-worker isolated workspace copies.
type WorkspaceBinder interface {
	CreateWorkerWorkspace(ctx context.Context, primaryDir, unitID string) (*workspace.Binding, error)
	DestroyWorkerWorkspace(ctx context.Context, binding *workspace.Binding) error
}

func (o *OrchestratorImpl) ensureWorkspaceManager(mode WorkspaceMode) error {
	if effectiveWorkspaceMode(mode) != WorkspaceIsolated {
		return nil
	}
	if o == nil || o.workspaces == nil {
		return fmt.Errorf("workspace_mode isolated requires WorkspaceManager")
	}
	return nil
}

func (o *OrchestratorImpl) bindUnitWorkspacesIfIsolated(
	ctx context.Context,
	spec TopologySpec,
	projectDir, batchID string,
	unitIDs []string,
) ([]*workspace.Binding, error) {
	if effectiveWorkspaceMode(spec.WorkspaceMode) != WorkspaceIsolated {
		return nil, nil
	}
	if err := o.ensureWorkspaceManager(spec.WorkspaceMode); err != nil {
		return nil, err
	}
	bindings := make([]*workspace.Binding, 0, len(unitIDs))
	for _, unitID := range unitIDs {
		unit, err := o.workers.GetUnit(ctx, batchID, unitID)
		if err != nil {
			o.destroyBindings(ctx, bindings)
			return nil, err
		}
		if unit.Status != api.LegStatusPending || unit.WorkspaceID != "" {
			continue
		}
		binding, err := o.workspaces.CreateWorkerWorkspace(ctx, projectDir, unitID)
		if err != nil {
			o.destroyBindings(ctx, bindings)
			return nil, err
		}
		unit.WorkspaceRoot = binding.Root
		unit.WorkspaceID = binding.ID
		if err := o.workers.UpdateUnit(ctx, batchID, *unit); err != nil {
			o.destroyBindings(ctx, append(bindings, binding))
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, nil
}

// BindLegWorkspacesIfIsolatedForTest exposes bindUnitWorkspacesIfIsolated for unit tests.
func (o *OrchestratorImpl) BindLegWorkspacesIfIsolatedForTest(
	ctx context.Context,
	spec TopologySpec,
	projectDir, batchID string,
	unitIDs []string,
) ([]*workspace.Binding, error) {
	return o.bindUnitWorkspacesIfIsolated(ctx, spec, projectDir, batchID, unitIDs)
}

func (o *OrchestratorImpl) destroyBindings(ctx context.Context, bindings []*workspace.Binding) {
	if o == nil || o.workspaces == nil {
		return
	}
	for _, b := range bindings {
		_ = o.workspaces.DestroyWorkerWorkspace(context.WithoutCancel(ctx), b)
	}
}
