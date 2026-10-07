package orchestration

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkspaceBinder creates and destroys per-leg sandbox copies (out-of-repo, git-free).
type WorkspaceBinder interface {
	CreateWorkerWorkspace(ctx context.Context, primaryDir, legID string) (*workspace.Binding, error)
	DestroyWorkerWorkspace(binding *workspace.Binding) error
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

func (o *OrchestratorImpl) bindLegWorkspacesIfIsolated(
	ctx context.Context,
	spec TopologySpec,
	projectDir, delegationID string,
	legIDs []string,
) ([]*workspace.Binding, error) {
	if effectiveWorkspaceMode(spec.WorkspaceMode) != WorkspaceIsolated {
		return nil, nil
	}
	if err := o.ensureWorkspaceManager(spec.WorkspaceMode); err != nil {
		return nil, err
	}
	bindings := make([]*workspace.Binding, 0, len(legIDs))
	for _, legID := range legIDs {
		leg, err := o.store.GetLeg(ctx, delegationID, legID)
		if err != nil {
			o.destroyBindings(bindings)
			return nil, err
		}
		if leg.Status != api.LegStatusPending || leg.WorkspaceID != "" {
			continue
		}
		binding, err := o.workspaces.CreateWorkerWorkspace(ctx, projectDir, legID)
		if err != nil {
			o.destroyBindings(bindings)
			return nil, err
		}
		leg.WorkspaceRoot = binding.Root
		leg.WorkspaceID = binding.ID
		if err := o.store.UpdateLeg(ctx, *leg); err != nil {
			o.destroyBindings(append(bindings, binding))
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, nil
}

// BindLegWorkspacesIfIsolatedForTest exposes bindLegWorkspacesIfIsolated for unit tests.
func (o *OrchestratorImpl) BindLegWorkspacesIfIsolatedForTest(
	ctx context.Context,
	spec TopologySpec,
	projectDir, delegationID string,
	legIDs []string,
) ([]*workspace.Binding, error) {
	return o.bindLegWorkspacesIfIsolated(ctx, spec, projectDir, delegationID, legIDs)
}

func (o *OrchestratorImpl) destroyBindings(bindings []*workspace.Binding) {
	if o == nil || o.workspaces == nil {
		return
	}
	for _, b := range bindings {
		_ = o.workspaces.DestroyWorkerWorkspace(b)
	}
}
