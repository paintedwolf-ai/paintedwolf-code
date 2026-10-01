package conditions

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// ShippedDelegateLeafIDs returns recipe-agnostic delegate step_kind gate leaves.
func ShippedDelegateLeafIDs() []string {
	return append([]string(nil), shippedDelegateLeafIDs...)
}

var shippedDelegateLeafIDs = []string{
	"recon_or_board_ready",
	"worker_cycle_ready",
	"topology_report_delivered",
	"fanout_planned",
}

// ShippedSubroutineLeafIDs returns subroutine invoke gate leaves.
func ShippedSubroutineLeafIDs() []string {
	return append([]string(nil), shippedSubroutineLeafIDs...)
}

var shippedSubroutineLeafIDs = []string{
	"child_run_complete",
	"child_run_failed",
}

// WorkerCycleIdle reports whether no pending/running jobs remain (excluding completingJobID).
type WorkerCycleIdle func(projectID, sessionID, completingJobID string) (bool, error)

// ChildRunStatusReader resolves active child run terminal status for a parent workflow run.
type ChildRunStatusReader func(parentRunID string) (status string, ok bool)

// RegisterDelegateDomain registers delegate + subroutine vocabulary evaluators.
func RegisterDelegateDomain(reg *ConditionRegistry, deps RegistryDeps) error {
	if reg == nil {
		return nil
	}
	for _, entry := range []struct {
		name   string
		fn     ConditionFunc
		active ConditionFunc
	}{
		{name: "recon_or_board_ready", fn: reconOrBoardReady},
		{name: "worker_cycle_ready", fn: workerCycleReady(deps), active: workerCycleActive},
		{name: "topology_report_delivered", fn: func(ec EvalContext) (bool, error) {
			return BoolVar(nestedMap(ec.Vars, "gates"), "topology_report_delivered"), nil
		}},
		{name: "fanout_planned", fn: func(ec EvalContext) (bool, error) {
			if BoolVar(nestedMap(ec.Vars, "gates"), "fanout_planned") {
				return true, nil
			}
			return fanoutPlanStamped(ec), nil
		}},
		{name: "child_run_complete", fn: childRunComplete(deps)},
		{name: "child_run_failed", fn: childRunFailed(deps)},
	} {
		var err error
		if entry.active != nil {
			err = reg.RegisterEventScoped(entry.name, entry.fn, entry.active)
		} else {
			err = reg.Register(entry.name, entry.fn)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// fanoutPlanStamped checks the current phase's stamped plan independently of peer phases.
func fanoutPlanStamped(ec EvalContext) bool {
	phase := strings.TrimSpace(ec.Phase)
	if phase == "" {
		return false
	}
	_, ok := nestedMap(ec.Vars, "fanout_plans")[phase]
	return ok
}

func workerCycleActive(ec EvalContext) (bool, error) {
	return BoolVar(nestedMap(ec.Vars, "worker_cycle"), "evaluating"), nil
}

func reconOrBoardReady(ec EvalContext) (bool, error) {
	board := nestedMap(ec.Vars, "board")
	return BoolVar(board, "orient_ready") || stringVar(board, "inject_key") != "", nil
}

func workerCycleReady(deps RegistryDeps) ConditionFunc {
	return func(ec EvalContext) (bool, error) {
		wc := nestedMap(ec.Vars, "worker_cycle")
		if !BoolVar(wc, "evaluating") {
			return false, nil
		}
		if fanoutPlanStamped(ec) {
			if !BoolVar(ec.Vars, "fanout_settled") {
				return false, nil
			}
		} else if !api.WorkerSummaryLegSucceeded(api.WorkerSummaryStatus(stringVar(wc, "summary_status"))) {
			return false, nil
		}
		if deps.WorkerCycleIdle != nil {
			idle, err := deps.WorkerCycleIdle(ec.ProjectID, ec.SessionID, stringVar(wc, "completing_job_id"))
			if err != nil {
				return false, err
			}
			if !idle {
				return false, nil
			}
		}
		return true, nil
	}
}

func childRunComplete(deps RegistryDeps) ConditionFunc {
	return func(ec EvalContext) (bool, error) {
		status := childRunStatusFromContext(ec, deps)
		return status == string(api.WorkflowRunStatusComplete), nil
	}
}

func childRunFailed(deps RegistryDeps) ConditionFunc {
	return func(ec EvalContext) (bool, error) {
		status := childRunStatusFromContext(ec, deps)
		switch status {
		case "failed", string(api.WorkflowRunStatusCanceled):
			return true, nil
		default:
			return false, nil
		}
	}
}

func childRunStatusFromContext(ec EvalContext, deps RegistryDeps) string {
	cr := nestedMap(ec.Vars, "child_run")
	if s := strings.TrimSpace(stringVar(cr, "status")); s != "" {
		return s
	}
	if deps.ChildRunStatus != nil && ec.WorkflowRunID != "" {
		if status, ok := deps.ChildRunStatus(ec.WorkflowRunID); ok {
			return strings.TrimSpace(status)
		}
	}
	return ""
}
