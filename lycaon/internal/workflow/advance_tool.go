package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// HostAutoAdvancedFromKey is set when the host auto-advances a workflow phase.
const HostAutoAdvancedFromKey = "host_auto_advanced_from"

// AdvanceToolResult is returned by workflow_advance. On phase_gate_unmet its
// fields mirror the HTTP 409 details body (phase, reason, failed_gate, failed_leaves).
type AdvanceToolResult struct {
	Run             *api.WorkflowRun `json:"run,omitempty"`
	AlreadyAdvanced bool             `json:"already_advanced,omitempty"`
	Error           string           `json:"error,omitempty"`
	FailedLeaves    []string         `json:"failed_leaves,omitempty"`
	FailedGate      string           `json:"failed_gate,omitempty"`
	Reason          string           `json:"reason,omitempty"`
	Phase           string           `json:"phase,omitempty"`
	PendingPhase    string           `json:"pending_phase,omitempty"`
	Message         string           `json:"message,omitempty"`
}

// RegisterAdvanceTool registers workflow_advance for coordinator sessions.
func RegisterAdvanceTool(reg *tools.DefaultRegistry, runs *RunManager) error {
	if reg == nil || runs == nil {
		return fmt.Errorf("registry and run manager required")
	}
	if err := reg.Register("workflow_advance", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if !isCoordinatorAgent(tctx.Identity.Agent) {
			return "", fmt.Errorf("workflow_advance requires coordinator role")
		}
		if len(args) > 0 {
			return "", fmt.Errorf("workflow_advance takes no arguments")
		}
		if err := requireSessionProject(ctx, runs.Sessions, tctx); err != nil {
			return "", err
		}
		if replayed, already, ok, replayErr := runs.replayAdvanceToolOperation(ctx, tctx.Identity.ToolCallID); replayErr != nil || ok {
			if already {
				return marshalAdvanceToolResult(tctx.Effects.Out, alreadyAdvancedResult(replayed, ""))
			}
			return marshalAdvanceToolOutcome(tctx.Effects.Out, replayed, replayErr)
		}
		active, err := runs.Store.ActiveBySession(ctx, tctx.Identity.SessionID)
		if err != nil {
			return "", err
		}
		if active == nil {
			return "", ErrNoActiveRun
		}
		vars, err := runs.Store.GetScaffoldVars(ctx, active.ID)
		if err != nil {
			return "", err
		}
		if scaffoldvars.HasPendingUserInput(vars) {
			return marshalAdvanceToolResult(tctx.Effects.Out, advanceBlockedPendingInput(vars, active.CurrentPhase))
		}
		manifest, err := runs.manifestForRun(ctx, active)
		if err != nil {
			return "", err
		}
		if def, ok := manifest.PhaseByID(active.CurrentPhase); ok {
			if workflowdef.EffectiveAdvancePolicy(manifest, def) != workflowdef.AdvanceWhenGateMetCoordinator {
				return marshalAdvanceToolResult(tctx.Effects.Out, AdvanceToolResult{
					Error:   "advance_not_coordinator_mode",
					Phase:   active.CurrentPhase,
					Message: fmt.Sprintf("phase %q uses host auto-advance; workflow_advance is not available on this phase", active.CurrentPhase),
				})
			}
		} else if manifest.Controls.PhaseAdvance.HostOnly() {
			return marshalAdvanceToolResult(tctx.Effects.Out, AdvanceToolResult{
				Error:   "advance_not_coordinator_mode",
				Phase:   active.CurrentPhase,
				Message: fmt.Sprintf("phase %q uses host auto-advance; workflow_advance is not available on this phase", active.CurrentPhase),
			})
		}
		if _, ok := hostAutoAdvancedFromPhase(vars); ok {
			if out, consumed, consumeErr := runs.consumeHostAutoAdvancedMarker(ctx, active.ID, tctx.Identity.ToolCallID); consumeErr != nil {
				return "", consumeErr
			} else if consumed {
				return marshalAdvanceToolResult(tctx.Effects.Out, out)
			}
			// The marker was consumed concurrently; fall through to a real advance
			// against the run's current revision.
			if active, err = runs.Store.Get(ctx, active.ID); err != nil {
				return "", err
			}
		}
		commandCtx := withWorkflowCommandOperation(WithExpectedRevision(ctx, active.Revision), tctx.Identity.ToolCallID)
		run, err := runs.Advance(commandCtx, active.ID)
		return marshalAdvanceToolOutcome(tctx.Effects.Out, run, err)
	}); err != nil {
		return err
	}
	return nil
}

// consumeHostAutoAdvancedMarker clears the host-auto-advanced marker through the command
// journal, so the AlreadyAdvanced receipt replays deterministically for duplicate tool
// calls. consumed is false when a concurrent call took the marker first.
func (m *RunManager) consumeHostAutoAdvancedMarker(ctx context.Context, runID, toolCallID string) (AdvanceToolResult, bool, error) {
	unlockVars := m.lockRunVars(runID)
	defer unlockVars()
	run, err := m.Store.Get(ctx, runID)
	if err != nil {
		return AdvanceToolResult{}, false, err
	}
	vars, err := m.Store.GetScaffoldVars(ctx, runID)
	if err != nil {
		return AdvanceToolResult{}, false, err
	}
	from, ok := hostAutoAdvancedFromPhase(vars)
	if !ok {
		return AdvanceToolResult{}, false, nil
	}
	commandCtx := withWorkflowCommandOperation(ctx, toolCallID)
	if err := m.commitCommand(commandCtx, run, advanceAlreadyCommandKind, struct{}{}, clearHostAutoAdvancedMarker(vars), nil, "", workflowWorkerMutation{}, nil); err != nil {
		return AdvanceToolResult{}, false, err
	}
	return alreadyAdvancedResult(run, from), true, nil
}

func alreadyAdvancedResult(run *api.WorkflowRun, fromPhase string) AdvanceToolResult {
	result := AdvanceToolResult{
		Run:             run,
		AlreadyAdvanced: true,
		Message:         "host already advanced this phase",
	}
	if run != nil {
		result.Phase = run.CurrentPhase
	}
	if fromPhase != "" {
		result.Message = fmt.Sprintf("host already advanced from phase %q", fromPhase)
	}
	return result
}

func marshalAdvanceToolOutcome(out *tools.ToolInvocationOut, run *api.WorkflowRun, err error) (string, error) {
	if err == nil {
		return marshalAdvanceToolResult(out, AdvanceToolResult{Run: run})
	}
	if gateErr, ok := IsPhaseGateUnmet(err); ok {
		return marshalAdvanceToolResult(out, AdvanceToolResult{
			Error: "phase_gate_unmet", Phase: gateErr.Phase, Reason: gateErr.Reason,
			FailedGate: gateErr.FailedGate, FailedLeaves: append([]string(nil), gateErr.FailedLeaves...),
			Message: gateErr.Error(),
		})
	}
	return "", err
}

func marshalAdvanceToolResult(out *tools.ToolInvocationOut, result AdvanceToolResult) (string, error) {
	publishAdvanceToolOutcome(out, result)
	raw, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func publishAdvanceToolOutcome(out *tools.ToolInvocationOut, result AdvanceToolResult) {
	if out == nil {
		return
	}
	phase := strings.TrimSpace(result.Phase)
	switch result.Error {
	case "phase_gate_unmet":
		out.Facts = out.Facts.WithFeedback("WORKFLOW_GATE_BLOCKED", map[string]any{
			"phase": phase, "reason": result.Reason, "failed_gate": result.FailedGate,
			"failed_leaves": append([]string(nil), result.FailedLeaves...),
		}, nil)
		out.Completion = &api.ToolCompletion{Operation: "workflow_advance", State: "blocked"}
	case "advance_not_coordinator_mode":
		out.Facts = out.Facts.WithFeedback("WORKFLOW_ADVANCE_NOT_COORDINATOR_MODE", map[string]any{"phase": phase}, nil)
		out.Completion = &api.ToolCompletion{Operation: "workflow_advance", State: "unavailable"}
	case "pending_user_input":
		out.Completion = &api.ToolCompletion{Operation: "workflow_advance", State: "pending_input"}
	default:
		if result.Run != nil {
			out.Completion = &api.ToolCompletion{Operation: "workflow_advance", State: "advanced", ResourceKind: "workflow_run", ResourceID: strings.TrimSpace(result.Run.ID)}
		}
	}
}

func advanceBlockedPendingInput(vars map[string]any, currentPhase string) AdvanceToolResult {
	result := AdvanceToolResult{
		Error:   "pending_user_input",
		Phase:   currentPhase,
		Message: "advance blocked while user feedback or a decision is pending",
	}
	if phase, ok := pendingFeedbackPhase(vars); ok {
		result.PendingPhase = phase
		result.Message = fmt.Sprintf("advance blocked: pending user feedback on phase %q", phase)
	}
	return result
}

func hostAutoAdvancedFromPhase(vars map[string]any) (string, bool) {
	if vars == nil {
		return "", false
	}
	raw, ok := vars[HostAutoAdvancedFromKey]
	if !ok {
		return "", false
	}
	phase, _ := raw.(string)
	phase = strings.TrimSpace(phase)
	if phase == "" {
		return "", false
	}
	return phase, true
}

func clearHostAutoAdvancedMarker(vars map[string]any) map[string]any {
	vars = cloneVars(vars)
	delete(vars, HostAutoAdvancedFromKey)
	return vars
}

func stampHostAutoAdvancedFrom(vars map[string]any, fromPhase string) map[string]any {
	fromPhase = strings.TrimSpace(fromPhase)
	if fromPhase == "" {
		return vars
	}
	return SetHostVar(vars, HostAutoAdvancedFromKey, fromPhase)
}
