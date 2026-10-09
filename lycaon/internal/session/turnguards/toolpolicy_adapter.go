package turnguards

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolfeedback"

	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) HasComposeDraft(ctx context.Context, sess *api.Session) bool {
	if m == nil || m.coordinatorFrame == nil || sess == nil {
		return false
	}
	frame, err := m.coordinatorFrame.BuildCoordinatorTurnFrame(ctx, sess.ID, sess)
	if err != nil {
		return false
	}
	return frame.RunContext.HasComposeDraft
}

func (m *Service) PolicyDependencies() toolpolicy.EngineDeps {
	posturesFn := func(ctx context.Context, sess *api.Session) (toolpolicy.PostureRegistry, error) {
		reg, err := m.Profiles.EffectivePostures(ctx, sess)
		if err != nil {
			return nil, err
		}
		if reg == nil {
			return nil, nil
		}
		return reg, nil
	}
	return toolpolicy.EngineDeps{
		ToolLister:       m.toolLister,
		Rules:            m.rules,
		Workflows:        m.policyWorkflows(),
		Postures:         posturesFn,
		Limits:           m.Limits.Effective,
		HasComposeDraft:  m.HasComposeDraft,
		ToolAccess:       m.Profiles.ResolveToolAccess,
		RejectFormatter:  m.toolRejectFormatter,
		BlockPlane:       &toolfeedback.BlockPlane{Pipeline: m.ToolPolicy.Pipeline, Renderer: m.Feedback.Renderer()},
		PreInvoke:        m.BeforeInvoke,
		ProjectRootCount: m.Workspace.RootCount,
		OverlayRootPaths: m.Workspace.SettingsRoots,
	}
}

func (m *Service) BeforeInvoke(_ context.Context, sess *api.Session, toolName string, args map[string]any) error {
	if m == nil || sess == nil {
		return nil
	}
	if m.profileRuntimeRules != nil {
		if err := m.profileRuntimeRules.EvaluateCoordinator(sess, toolName, args, m.Rejects); err != nil {
			return err
		}
	}
	return nil
}

func (m *Service) policyWorkflows() *toolpolicy.WorkflowDomains {
	if m.workflows == nil {
		return nil
	}
	return &toolpolicy.WorkflowDomains{Policy: m.workflows.Policy, Blueprints: m.workflows.Blueprints, Runs: m.workflows.Runs}
}
