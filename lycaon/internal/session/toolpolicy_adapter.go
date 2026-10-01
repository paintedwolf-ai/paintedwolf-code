package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type toolpolicyWorkflowView struct {
	v WorkflowSessionView
}

func (a toolpolicyWorkflowView) CurrentPhase(ctx context.Context, sessionID string) string {
	if a.v == nil {
		return ""
	}
	return a.v.CurrentPhase(ctx, sessionID)
}

func (a toolpolicyWorkflowView) ActivePhaseHasReviewLoop(ctx context.Context, sessionID string) bool {
	if a.v == nil {
		return false
	}
	return a.v.ActivePhaseHasReviewLoop(ctx, sessionID)
}

func (a toolpolicyWorkflowView) AllowedAgents(ctx context.Context, sessionID string) []string {
	if a.v == nil {
		return nil
	}
	return a.v.AllowedAgents(ctx, sessionID)
}

func (a toolpolicyWorkflowView) ActiveManifest(ctx context.Context, sessionID string) (toolpolicy.ActiveWorkflowManifest, bool) {
	if a.v == nil {
		return toolpolicy.ActiveWorkflowManifest{}, false
	}
	m, ok := a.v.ActiveManifest(ctx, sessionID)
	if !ok {
		return toolpolicy.ActiveWorkflowManifest{}, false
	}
	return toolpolicy.ActiveWorkflowManifest{
		CoordinatorProfile: m.CoordinatorProfile,
		Rules:              m.Rules,
		HostPhaseAdvance:   m.HostPhaseAdvance,
	}, true
}

func (a toolpolicyWorkflowView) ScaffoldVarsForSession(ctx context.Context, sessionID string) (map[string]any, error) {
	if a.v == nil {
		return nil, nil
	}
	return a.v.ScaffoldVarsForSession(ctx, sessionID)
}

func (a toolpolicyWorkflowView) ActivePlan(ctx context.Context, sessionID string) (string, string, bool) {
	if a.v == nil {
		return "", "", false
	}
	return a.v.ActivePlan(ctx, sessionID)
}

func (a toolpolicyWorkflowView) GetActive(ctx context.Context, sessionID string) (*api.WorkflowRun, error) {
	if a.v == nil {
		return nil, nil
	}
	return a.v.GetActive(ctx, sessionID)
}

type postureRegistryAdapter struct {
	reg *PostureRegistry
}

func (a postureRegistryAdapter) RulesPaths(posture api.SessionPosture) ([]string, error) {
	if a.reg == nil {
		return nil, nil
	}
	return a.reg.RulesPaths(posture)
}

func (m *Manager) hasComposeDraft(ctx context.Context, sess *api.Session) bool {
	if m == nil || m.coordinatorFrame == nil || sess == nil {
		return false
	}
	frame, err := m.coordinatorFrame.BuildCoordinatorTurnFrame(ctx, sess.ID, sess)
	if err != nil {
		return false
	}
	return frame.RunContext.HasComposeDraft
}

func (m *Manager) toolpolicyEngineDeps() toolpolicy.EngineDeps {
	posturesFn := func(ctx context.Context, sess *api.Session) (toolpolicy.PostureRegistry, error) {
		reg, err := m.effectivePostures(ctx, sess)
		if err != nil {
			return nil, err
		}
		return postureRegistryAdapter{reg: reg}, nil
	}
	return toolpolicy.EngineDeps{
		ToolInvoker:      m.toolInvoker,
		Rules:            m.rules,
		Workflows:        toolpolicyWorkflowView{v: m.workflows},
		Postures:         posturesFn,
		Limits:           m.effectiveLimits,
		HasComposeDraft:  m.hasComposeDraft,
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
