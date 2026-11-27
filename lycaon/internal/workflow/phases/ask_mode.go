package phases

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/session/store"
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

type askPhaseBlockedError struct {
	WorkflowID  string
	Phase       string
	TargetPhase string
}

func (e *askPhaseBlockedError) Error() string {
	return runstate.ErrAskWorkflowRequiresAgent.Error()
}

func (e *askPhaseBlockedError) Unwrap() error {
	return runstate.ErrAskWorkflowRequiresAgent
}

func (m *Service) checkAskPhase(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest, target string) error {
	ask, _ := ctx.Value(askRoundKey{}).(bool)
	if m.ResolveEffectiveMode == nil {
		return store.ErrSessionModeUnavailable
	}
	authority, err := m.ResolveEffectiveMode(ctx, run.SessionID)
	if err != nil {
		return err
	}
	mode, err := authority.WithModeIntent(nil)
	if err != nil {
		return err
	}
	ask = ask || mode == api.SessionModeAsk
	if ask && (runstate.IsAmbientRun(run) || target != "" && manifest.PhaseRequiresAgent(target)) {
		return &askPhaseBlockedError{
			WorkflowID:  run.WorkflowID,
			Phase:       run.CurrentPhase,
			TargetPhase: target,
		}
	}
	return nil
}

func askWorkflowToolError(err error, tool string) error {
	if !errors.Is(err, runstate.ErrAskWorkflowRequiresAgent) {
		return err
	}
	data := map[string]any{"tool": tool}
	var blocked *askPhaseBlockedError
	if errors.As(err, &blocked) {
		if blocked.WorkflowID != "" {
			data["workflow_id"] = blocked.WorkflowID
		}
		if blocked.Phase != "" {
			data["phase"] = blocked.Phase
		}
		if blocked.TargetPhase != "" {
			data["requested"] = blocked.TargetPhase
		}
	}
	return &toolrejection.ToolReject{Code: "SESSION_ASK_MODE_WORKFLOW_REQUIRES_AGENT", Data: data}
}
