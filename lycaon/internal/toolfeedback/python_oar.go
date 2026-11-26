package toolfeedback

import (
	"context"
	"maps"
	"strings"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/pythonevidence"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// PythonOARRecoveryDetailKey stores structured PY05 OAR recovery facts on a
// rendered refusal's details map without mutating policy copy or Invocation.Tool.
const PythonOARRecoveryDetailKey = "python_oar_recovery"

// OrdinaryResultObservation captures the outcome of an ordinary (non-rejection)
// execution of `python` or `command` for model-visible OAR recovery evaluation.
type OrdinaryResultObservation struct {
	Succeeded             bool
	RuntimeKind           string
	StageName             string
	ChildOperation        string
	StoppedOperationID    string
	StoppedOperation      string
	PriorCompletedEffects int
	ExitCode              int
	AdmittedProjectRunner string
	ReadOnly              bool
}

// EvaluatePythonOrdinaryResult evaluates an ordinary tool result against the
// frozen toolreach.Request on ctx. Successful exchanges return nil so they stay
// free of additional routing copy.
func EvaluatePythonOrdinaryResult(
	ctx context.Context,
	initiatingTool string,
	obs OrdinaryResultObservation,
) *pythonevidence.OARRecoveryFacts {
	if obs.Succeeded {
		return nil
	}
	gc := oar.NewGuardContext()
	gc.ObserveToolCall(initiatingTool, nil)
	fillOccurrenceIdentity(ctx, gc)
	readOnly := obs.ReadOnly || isReadOnlyGuard(gc)
	surface := pythonevidence.ReachableSurfaceFromRequest(gc.Reach, readOnly, obs.AdmittedProjectRunner)
	code := ordinaryStopCode(gc.Invocation.Tool, obs.RuntimeKind)
	facts := pythonevidence.BuildOARRecoveryFacts(surface, pythonevidence.RecoveryStopContext{
		PolicyCode:            code,
		InitiatingTool:        gc.Invocation.Tool,
		StageName:             obs.StageName,
		ChildOperation:        obs.ChildOperation,
		RuntimeKind:           obs.RuntimeKind,
		StoppedOperationID:    obs.StoppedOperationID,
		StoppedOperation:      obs.StoppedOperation,
		PriorCompletedEffects: obs.PriorCompletedEffects,
		ExitCode:              obs.ExitCode,
	})
	return &facts
}

func ordinaryStopCode(tool, runtimeKind string) pythonruntime.StopCode {
	switch runtimeKind {
	case pythonevidence.RuntimeKindProjectRunner:
		return pythonevidence.StopProjectRunnerFailure
	case pythonevidence.RuntimeKindShellStage:
		return pythonevidence.StopShellStageFailure
	case pythonevidence.RuntimeKindPortablePython:
		return pythonevidence.StopPythonException
	default:
		if strings.TrimSpace(tool) == "python" {
			return pythonevidence.StopPythonException
		}
		return ""
	}
}

func attachPythonOARRecovery(gc *oar.GuardContext, tr *toolrejection.ToolReject, details map[string]any) map[string]any {
	if gc == nil || tr == nil || !needsPythonOARRecovery(gc, tr) {
		return details
	}
	admittedRunner := stringFromMap(tr.Data, "admitted_project_runner")
	surface := pythonevidence.ReachableSurfaceFromRequest(gc.Reach, isReadOnlyGuard(gc), admittedRunner)
	facts := pythonevidence.BuildOARRecoveryFacts(surface, recoveryStopContextFromGuard(gc, tr))

	merged := maps.Clone(details)
	if merged == nil {
		merged = map[string]any{}
	}
	merged[PythonOARRecoveryDetailKey] = facts
	return merged
}

func needsPythonOARRecovery(gc *oar.GuardContext, tr *toolrejection.ToolReject) bool {
	code := pythonruntime.StopCode(strings.TrimSpace(tr.Code))
	switch code {
	case pythonevidence.StopPythonToolRequired,
		pythonruntime.StopEffectNotAllowed,
		pythonruntime.StopRevisionConflict,
		pythonevidence.StopHTTPHostDenied,
		pythonevidence.StopWritePathDenied:
		return true
	}
	return strings.TrimSpace(gc.Invocation.Tool) == "python"
}

func isReadOnlyGuard(gc *oar.GuardContext) bool {
	if gc == nil {
		return false
	}
	posture := strings.ToLower(strings.TrimSpace(gc.Session.SessionPosture))
	profile := strings.ToLower(strings.TrimSpace(gc.Session.PermissionProfile))
	return posture == "ask" || posture == "read_only" || strings.Contains(profile, "readonly") || strings.Contains(profile, "read_only")
}

func recoveryStopContextFromGuard(gc *oar.GuardContext, tr *toolrejection.ToolReject) pythonevidence.RecoveryStopContext {
	data := tr.Data
	stoppedOp := stringFromMap(data, "stopped_operation")
	if stoppedOp == "" {
		stoppedOp = stringFromMap(data, "command")
	}
	target := stringFromMap(data, "path")
	if target == "" {
		target = stringFromMap(data, "target")
	}
	return pythonevidence.RecoveryStopContext{
		PolicyCode:            pythonruntime.StopCode(strings.TrimSpace(tr.Code)),
		InitiatingTool:        gc.Invocation.Tool,
		StageName:             stringFromMap(data, "stage_name"),
		ChildOperation:        stringFromMap(data, "child_operation"),
		RuntimeKind:           stringFromMap(data, "runtime_kind"),
		StoppedOperationID:    stringFromMap(data, "stopped_operation_id"),
		StoppedOperation:      stoppedOp,
		PriorCompletedEffects: intFromMap(data, "prior_completed_effects"),
		ExitCode:              intFromMap(data, "exit_code"),
		HeldOperationID:       stringFromMap(data, "held_operation_id"),
		Target:                target,
	}
}

func stringFromMap(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return strings.TrimSpace(v)
}

func intFromMap(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}
