package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type postureRegistryAdapter struct {
	reg *PostureRegistry
}

func (a postureRegistryAdapter) RulesPaths(posture api.SessionPosture) ([]string, error) {
	if a.reg == nil {
		return nil, nil
	}
	return a.reg.RulesPaths(posture)
}

func (m *Manager) toolpolicyEngineDeps() toolpolicy.EngineDeps {
	posturesFn := func(ctx context.Context, sess *api.Session) (toolpolicy.PostureRegistry, error) {
		reg, err := m.effectivePostures(ctx, sess)
		if err != nil {
			return nil, err
		}
		return postureRegistryAdapter{reg: reg}, nil
	}
	var workflowSource toolpolicy.WorkflowSource
	if m.workflows != nil && m.workflows.Policy != nil {
		workflowSource = m.workflows.Policy.PolicySnapshot
	}
	return toolpolicy.EngineDeps{
		ToolInvoker:      m.toolInvoker,
		Rules:            m.rules,
		Workflows:        workflowSource,
		Postures:         posturesFn,
		ToolAccess:       m.ResolveToolAccess,
		RejectFormatter:  m.toolRejectFormatter,
		BlockPlane:       &tools.BlockPlane{Pipeline: m.oarPipeline, Renderer: m.oarRenderer},
		PreInvoke:        m.coordinatorPreInvoke,
		ProjectRootCount: m.projectRootCount,
		OverlayRootPaths: m.overlayRootPaths,
	}
}

func (m *Manager) coordinatorPreInvoke(_ context.Context, sess *api.Session, toolName string, args map[string]any) error {
	if m == nil || sess == nil {
		return nil
	}
	if m.profileRuntimeRules != nil {
		if err := m.profileRuntimeRules.EvaluateCoordinator(sess, toolName, args, m.rejectFmt); err != nil {
			return err
		}
	}
	return nil
}
