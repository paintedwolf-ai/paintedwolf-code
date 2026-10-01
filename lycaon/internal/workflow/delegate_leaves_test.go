package workflow

import (
	"testing"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestSetWorkerCycleEvalVars(t *testing.T) {
	vars := SetWorkerCycleEvalVars(nil, "job-1", "complete")
	wc, _ := vars["worker_cycle"].(map[string]any)
	if wc == nil || wc["evaluating"] != true {
		t.Fatalf("worker_cycle = %v", wc)
	}
	if wc["completing_job_id"] != "job-1" || wc["summary_status"] != "complete" {
		t.Fatalf("worker_cycle fields = %v", wc)
	}
	cleared := ClearWorkerCycleEvalVars(vars)
	if _, ok := cleared["worker_cycle"]; ok {
		t.Fatal("expected worker_cycle cleared")
	}
}

func TestDelegateHostVarWriters(t *testing.T) {
	board := SetBoardOrientReadyVar(nil, "fp-1")
	bm, _ := board["board"].(map[string]any)
	if bm["orient_ready"] != true || bm["inject_key"] != "fp-1" {
		t.Fatalf("board = %v", bm)
	}
	child := SetChildRunStatusVar(nil, "complete")
	cr, _ := child["child_run"].(map[string]any)
	if cr["status"] != "complete" {
		t.Fatalf("child_run = %v", cr)
	}
}

func TestRegisteredGateLeafIDsIncludesDelegateLeaves(t *testing.T) {
	ids := workflowdef.RegisteredGateLeafIDs()
	found := map[string]bool{}
	for _, id := range ids {
		found[id] = true
	}
	for _, id := range workflowdef.DelegateSubroutineGateLeafIDs() {
		if !found[id] {
			t.Fatalf("RegisteredGateLeafIDs missing %q", id)
		}
	}
}

func TestImplementWorkLegKey(t *testing.T) {
	key := ImplementWorkLegKey("sess-abc")
	if key != "implement-work:sess-abc" {
		t.Fatalf("leg key = %q", key)
	}
}
