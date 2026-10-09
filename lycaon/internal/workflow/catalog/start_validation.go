package catalog

import (
	"context"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Resolver) ValidateUserFacingStart(ctx context.Context, projectDir, sessionID, workflowID, version string) error {
	if m == nil {
		return workflowdef.ErrUnknownWorkflow
	}
	if m.Overlay != nil && m.Overlay.CatalogStartable(workflowID, version) {
		return nil
	}
	registry, scopes, err := m.Resolve(ctx, projectDir, sessionID)
	if err != nil {
		return workflowdef.ErrUnknownWorkflow
	}
	manifest, err := registry.Get(workflowID, version)
	if err != nil || manifest.Retired {
		return workflowdef.ErrUnknownWorkflow
	}
	switch scopes[workflowdef.ManifestKey(workflowID, version)] {
	case string(api.WorkflowScopeSession), string(api.WorkflowScopeProject):
		return nil
	default:
		return workflowdef.ErrUnknownWorkflow
	}
}
