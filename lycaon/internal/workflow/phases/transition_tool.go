package phases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/workflow/toolguard"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// TransitionToolResult is returned by workflow_transition on success.
type TransitionToolResult struct {
	Run *api.WorkflowRun `json:"run,omitempty"`
}

// RegisterTransitionTool registers workflow_transition for coordinator sessions.
func RegisterTransitionTool(reg *tools.DefaultRegistry, runs *Service) error {
	if reg == nil || runs == nil {
		return fmt.Errorf("registry and run manager required")
	}
	return reg.Register("workflow_transition", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if !toolguard.IsCoordinatorAgent(tctx.Identity.Agent) {
			return "", fmt.Errorf("workflow_transition requires coordinator role")
		}
		if err := toolguard.RequireSessionProject(ctx, runs.Sessions, tctx); err != nil {
			return "", err
		}
		transitionID, _ := args["transition_id"].(string)
		transitionID = strings.TrimSpace(transitionID)
		if transitionID == "" {
			return "", &toolrejection.ToolReject{
				Code: "WORKFLOW_TRANSITION_UNKNOWN",
				Data: map[string]any{"detail": "transition_id required"},
			}
		}
		payload := struct {
			TransitionID string `json:"transition_id"`
			Actor        string `json:"actor"`
		}{TransitionID: transitionID, Actor: workflowdef.TransitionActorCoordinator}
		if replayed, ok, replayErr := runs.Journal.ReplayOperation(ctx, tctx.Identity.ToolCallID, "fire_transition", payload); replayErr != nil || ok {
			if replayErr != nil {
				return "", replayErr
			}
			return marshalTransitionToolResult(TransitionToolResult{Run: replayed})
		}
		active, err := runs.Runs.ActiveBySession(ctx, tctx.Identity.SessionID)
		if err != nil {
			return "", err
		}
		if active == nil {
			return "", &toolrejection.ToolReject{
				Code: "WORKFLOW_TRANSITION_INACTIVE",
				Data: map[string]any{"detail": "no active workflow run"},
			}
		}
		commandCtx := runstate.WithCommandOperation(runstate.WithExpectedRevision(ctx, active.Revision), tctx.Identity.ToolCallID)
		run, err := runs.FireTransition(commandCtx, active.ID, transitionID, workflowdef.TransitionActorCoordinator)
		if err != nil {
			return "", mapTransitionToolError(err, transitionID, active.CurrentPhase)
		}
		return marshalTransitionToolResult(TransitionToolResult{Run: run})
	})
}

func mapTransitionToolError(err error, transitionID, phase string) error {
	switch {
	case errors.Is(err, runstate.ErrTransitionPendingInput):
		// Pending input uses the shared workflow hint.
		return &toolrejection.ToolReject{
			Code: "WORKFLOW_FEEDBACK_PENDING",
			Data: map[string]any{
				"phase":         phase,
				"transition_id": transitionID,
				"detail":        "choice leave blocked while user feedback or a decision is pending",
			},
		}
	case errors.Is(err, runstate.ErrTransitionUnknown):
		return &toolrejection.ToolReject{
			Code: "WORKFLOW_TRANSITION_UNKNOWN",
			Data: map[string]any{"transition_id": transitionID, "phase": phase},
		}
	case errors.Is(err, runstate.ErrTransitionActorDenied):
		return &toolrejection.ToolReject{
			Code: "WORKFLOW_TRANSITION_ACTOR_DENIED",
			Data: map[string]any{"transition_id": transitionID, "phase": phase},
		}
	case errors.Is(err, runstate.ErrTransitionNotArmed):
		return &toolrejection.ToolReject{
			Code: "WORKFLOW_TRANSITION_NOT_ARMED",
			Data: map[string]any{"transition_id": transitionID, "phase": phase},
		}
	default:
		if nr, ok := runstate.IsNotRunnable(err); ok {
			return &toolrejection.ToolReject{
				Code: "WORKFLOW_TRANSITION_INACTIVE",
				Data: map[string]any{"detail": nr.Error(), "phase": phase},
			}
		}
		return err
	}
}

func marshalTransitionToolResult(result TransitionToolResult) (string, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
