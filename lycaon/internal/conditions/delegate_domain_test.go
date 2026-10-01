package conditions

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func delegateTestRegistry(t *testing.T, deps RegistryDeps) *ConditionRegistry {
	t.Helper()
	reg, err := NewDefaultRegistry(deps)
	testutil.FailErr(t, "NewDefaultRegistry", err)
	return reg
}

func TestReconOrBoardReadyFromVars(t *testing.T) {
	reg := delegateTestRegistry(t, RegistryDeps{})
	ok, err := reg.Evaluate("recon_or_board_ready", EvalContext{
		Vars: map[string]any{"board": map[string]any{"orient_ready": true}},
	})
	testutil.FailErr(t, "Evaluate recon_or_board_ready", err)
	if !ok {
		t.Fatal("expected orient_ready true")
	}
	ok, err = reg.Evaluate("recon_or_board_ready", EvalContext{Vars: map[string]any{}})
	testutil.FailErr(t, "Evaluate recon_or_board_ready empty", err)
	if ok {
		t.Fatal("expected false without board vars")
	}
}

func TestWorkerCycleReadyRequiresEvalHookAndProof(t *testing.T) {
	reg := delegateTestRegistry(t, RegistryDeps{
		WorkerCycleIdle: func(_, _, _ string) (bool, error) { return true, nil },
	})
	vars := map[string]any{
		"worker_cycle": map[string]any{
			"evaluating":        true,
			"summary_status":    "complete",
			"completing_job_id": "job-1",
		},
	}
	ok, err := reg.Evaluate("worker_cycle_ready", EvalContext{Vars: vars})
	testutil.FailErr(t, "Evaluate worker_cycle_ready", err)
	if !ok {
		t.Fatal("expected worker cycle ready")
	}
	applicable, err := reg.Applicable("worker_cycle_ready", EvalContext{Vars: vars})
	testutil.FailErr(t, "Applicable worker_cycle_ready", err)
	if !applicable {
		t.Fatal("worker cycle gate must be applicable during terminal proof evaluation")
	}
	ok, err = reg.Evaluate("worker_cycle_ready", EvalContext{Vars: map[string]any{
		"worker_cycle": map[string]any{"evaluating": true, "summary_status": "partial"},
	}})
	testutil.FailErr(t, "Evaluate worker_cycle_ready partial", err)
	if ok {
		t.Fatal("partial summary must not satisfy worker_cycle_ready")
	}
	ok, err = reg.Evaluate("worker_cycle_ready", EvalContext{Vars: map[string]any{
		"worker_cycle": map[string]any{
			"evaluating":        true,
			"summary_status":    "open",
			"completing_job_id": "job-1",
		},
	}})
	testutil.FailErr(t, "Evaluate worker_cycle_ready open", err)
	if !ok {
		t.Fatal("open summary should satisfy worker_cycle_ready")
	}
	ok, err = reg.Evaluate("worker_cycle_ready", EvalContext{Vars: map[string]any{}})
	testutil.FailErr(t, "Evaluate worker_cycle_ready no hook", err)
	if ok {
		t.Fatal("user message path must not satisfy worker_cycle_ready without evaluating hook")
	}
	applicable, err = reg.Applicable("worker_cycle_ready", EvalContext{Vars: map[string]any{}})
	testutil.FailErr(t, "Applicable worker_cycle_ready no hook", err)
	if applicable {
		t.Fatal("worker cycle gate must be dormant outside terminal proof evaluation")
	}
	applicable, err = reg.Applicable("recon_or_board_ready", EvalContext{Vars: map[string]any{}})
	testutil.FailErr(t, "Applicable recon_or_board_ready", err)
	if !applicable {
		t.Fatal("ordinary gates must remain applicable")
	}
}

func TestWorkerCycleReadyRequiresIdleCycle(t *testing.T) {
	reg := delegateTestRegistry(t, RegistryDeps{
		WorkerCycleIdle: func(_, _, excluding string) (bool, error) {
			return excluding == "job-1", nil
		},
	})
	vars := map[string]any{
		"worker_cycle": map[string]any{
			"evaluating":        true,
			"summary_status":    "complete",
			"completing_job_id": "job-1",
		},
	}
	ok, err := reg.Evaluate("worker_cycle_ready", EvalContext{Vars: vars})
	testutil.FailErr(t, "Evaluate worker_cycle_ready", err)
	if !ok {
		t.Fatal("expected idle cycle")
	}
	regBusy := delegateTestRegistry(t, RegistryDeps{
		WorkerCycleIdle: func(_, _, _ string) (bool, error) { return false, nil },
	})
	ok, err = regBusy.Evaluate("worker_cycle_ready", EvalContext{Vars: vars})
	testutil.FailErr(t, "Evaluate worker_cycle_ready busy", err)
	if ok {
		t.Fatal("in-flight jobs must block worker_cycle_ready")
	}
}

func TestChildRunCompleteAndFailed(t *testing.T) {
	reg := delegateTestRegistry(t, RegistryDeps{
		ChildRunStatus: func(parentRunID string) (string, bool) {
			if parentRunID == "run-ok" {
				return string(api.WorkflowRunStatusComplete), true
			}
			if parentRunID == "run-bad" {
				return "failed", true
			}
			return "", false
		},
	})
	ok, err := reg.Evaluate("child_run_complete", EvalContext{WorkflowRunID: "run-ok"})
	testutil.FailErr(t, "Evaluate child_run_complete", err)
	if !ok {
		t.Fatal("expected child complete")
	}
	ok, err = reg.Evaluate("child_run_failed", EvalContext{WorkflowRunID: "run-bad"})
	testutil.FailErr(t, "Evaluate child_run_failed", err)
	if !ok {
		t.Fatal("expected child failed")
	}
	ok, err = reg.Evaluate("child_run_complete", EvalContext{
		Vars: map[string]any{"child_run": map[string]any{"status": "complete"}},
	})
	testutil.FailErr(t, "Evaluate child_run_complete vars", err)
	if !ok {
		t.Fatal("expected vars child complete")
	}
}

func TestDelegateLeavesRegisteredInDefaultRegistry(t *testing.T) {
	reg := delegateTestRegistry(t, RegistryDeps{})
	for _, id := range append(ShippedDelegateLeafIDs(), ShippedSubroutineLeafIDs()...) {
		if !reg.Has(id) {
			t.Fatalf("missing registered leaf %q", id)
		}
	}
}
