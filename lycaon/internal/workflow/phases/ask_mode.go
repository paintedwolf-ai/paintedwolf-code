package phases

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/toolrejection"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

type askRoundKey struct{}

func withAskRound(ctx context.Context, ask bool) context.Context {
	if !ask {
		return ctx
	}
	return context.WithValue(ctx, askRoundKey{}, true)
}

func (m *Service) checkAskPhase(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest, target string) error {
	ask, _ := ctx.Value(askRoundKey{}).(bool)
	if m.ResolveEffectiveMode != nil {
		authority, err := m.ResolveEffectiveMode(ctx, run.SessionID)
		if err != nil {
			return err
		}
		ask = ask || authority.Mode == api.SessionModeAsk
	}
	if ask && (runstate.IsAmbientRun(run) || target != "" && manifest.PhaseRequiresAgent(target)) {
		return runstate.ErrAskWorkflowRequiresAgent
	}
	return nil
}

func askWorkflowToolError(err error, tool string) error {
	if !errors.Is(err, runstate.ErrAskWorkflowRequiresAgent) {
		return err
	}
	return &toolrejection.ToolReject{Code: "SESSION_ASK_MODE_WORKFLOW_REQUIRES_AGENT", Data: map[string]any{"tool": tool}}
}
