package toolfeedback

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/pythonevidence"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// publishPythonOARFacts feeds structured PY05 recovery facts into GuardContext
// BEFORE OAR rule evaluation and pack rendering so the pack itself consumes
// the facts and renders the single authoritative model-visible advice.
// Nothing is serialized as a second routing object in refusal details.
func publishPythonOARFacts(gc *oar.GuardContext, tr *toolrejection.ToolReject) {
	if gc == nil || tr == nil || !needsPythonOARRecovery(gc, tr) {
		return
	}
	admittedRunner := stringFromMap(tr.Data, "admitted_project_runner")
	surface := pythonevidence.ReachableSurfaceFromRequest(gc.Reach, isReadOnlyPosture(gc, tr), admittedRunner)
	facts := pythonevidence.BuildOARRecoveryFacts(surface, recoveryStopContextFromGuard(gc, tr))

	if gc.ObservationData == nil {
		gc.ObservationData = map[string]any{}
	}
	if facts.StoppedOperation != "" && stringFromMap(gc.ObservationData, "command") == "" {
		gc.ObservationData["command"] = facts.StoppedOperation
	}
	if facts.Conflict != nil {
		if stringFromMap(gc.ObservationData, "path") == "" && facts.Conflict.Target.RelPath != "" {
			gc.ObservationData["path"] = facts.Conflict.Target.RelPath
		}
		if stringFromMap(gc.ObservationData, "expected_revision") == "" && facts.Conflict.ExpectedRevision != "" {
			gc.ObservationData["expected_revision"] = facts.Conflict.ExpectedRevision
		}
		if stringFromMap(gc.ObservationData, "observed_revision") == "" && facts.Conflict.ActualRevision != "" {
			gc.ObservationData["observed_revision"] = facts.Conflict.ActualRevision
		}
		if _, exists := gc.ObservationData["revision_change_observed"]; !exists {
			gc.ObservationData["revision_change_observed"] = true
		}
	}
	if facts.PendingHeldOperationID != "" {
		reason := stringFromMap(gc.ObservationData, "rejection_reason")
		if !strings.Contains(reason, facts.PendingHeldOperationID) {
			if reason != "" {
				reason += " "
			}
			gc.ObservationData["rejection_reason"] = fmt.Sprintf("%sPending approval operation: %s.", reason, facts.PendingHeldOperationID)
		}
	}
	if facts.PolicyCode == pythonruntime.StopEffectNotAllowed && surface.ReadOnly && stringFromMap(gc.ObservationData, "expected_form") == "" {
		gc.ObservationData["expected_form"] = "read_only"
	}
}

// needsPythonOARRecovery scopes PY05 OAR fact enrichment strictly to python
// invocations, PYTHON_TOOL_REQUIRED, or command invocations at Python/HTTP/
// workspace-effect boundaries. Unrelated tools (write, edit, read, etc.) are
// never matched on shared codes.
func needsPythonOARRecovery(gc *oar.GuardContext, tr *toolrejection.ToolReject) bool {
	if gc == nil || tr == nil {
		return false
	}
	tool := strings.TrimSpace(gc.Invocation.Tool)
	code := pythonruntime.StopCode(strings.TrimSpace(tr.Code))
	if code == pythonevidence.StopPythonToolRequired {
		return true
	}
	if tool == "python" {
		return true
	}
	if tool != "command" {
		return false
	}
	switch code {
	case pythonruntime.StopEffectNotAllowed,
		pythonruntime.StopRevisionConflict,
		pythonevidence.StopHTTPHostDenied,
		pythonevidence.StopWritePathDenied:
		return true
	default:
		return false
	}
}

// isReadOnlyPosture determines read-only posture strictly from resolved
// session posture, worker mutation capability, and structured effect facts —
// never from profile-name substring heuristics.
func isReadOnlyPosture(gc *oar.GuardContext, tr *toolrejection.ToolReject) bool {
	if gc != nil {
		posture := strings.ToLower(strings.TrimSpace(gc.Session.SessionPosture))
		if posture == "ask" || posture == "read_only" {
			return true
		}
		if gc.Session.WorkerLeg && !gc.Workers.ProfileMutationCapable {
			return true
		}
	}
	if tr != nil && tr.Data != nil {
		if ro, ok := tr.Data["read_only"].(bool); ok && ro {
			return true
		}
		if form := strings.ToLower(stringFromMap(tr.Data, "expected_form")); form == "read_only" {
			return true
		}
	}
	return false
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
	var conflict *pythonruntime.RevisionConflict
	if exp, obs := stringFromMap(data, "expected_revision"), stringFromMap(data, "observed_revision"); exp != "" || obs != "" {
		conflict = &pythonruntime.RevisionConflict{
			Target:           pythonruntime.CanonicalTarget{RelPath: target},
			ExpectedRevision: exp,
			ActualRevision:   obs,
		}
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
		Conflict:              conflict,
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
