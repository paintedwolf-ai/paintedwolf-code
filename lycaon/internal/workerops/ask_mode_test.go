package workerops_test

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerops"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workerstate"
)

func TestAskInstructionAdmissionUsesTheStoredWorkerScope(t *testing.T) {
	f := newFixture(t)
	agents := f.svc.Agents
	f.svc.Agents = func(name string) (bool, bool) {
		if name == "path-explorer" {
			return false, true
		}
		return agents(name)
	}
	_, err := f.records.CreateWorker(t.Context(), f.coord, op("write-worker"), workerrecords.WorkerSpec{
		Name: "write-worker", AgentType: "repo-researcher", ScopeMode: workerrecords.ScopeWrite,
	})
	testutil.FailErr(t, "record worker with write scope", err)

	for _, tc := range []struct {
		name, worker, agent string
		denied              bool
	}{
		{name: "current profile", worker: " surveyor "},
		{name: "same profile", worker: "surveyor", agent: "repo-researcher"},
		{name: "read profile", worker: "surveyor", agent: " path-explorer "},
		{name: "write profile", worker: "surveyor", agent: "implementer", denied: true},
		{name: "unknown profile", worker: "surveyor", agent: "unknown", denied: true},
		{name: "write scope", worker: "write-worker", agent: "path-explorer", denied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := f.svc.CheckAskInstruction(t.Context(), f.coord.SessionID, map[string]any{"worker": tc.worker, "agent_type": tc.agent})
			if tc.denied {
				requireAskWorkerRefusal(t, err)
			} else {
				testutil.FailErr(t, "check read worker", err)
			}
		})
	}
	err = f.svc.CheckAskInstruction(t.Context(), f.coord.SessionID, map[string]any{"worker": "missing"})
	if !errors.Is(err, workerrecords.ErrWorkerNotFound) {
		t.Fatalf("missing worker lookup = %v", err)
	}
}

func TestAskInstructionRefusalLeavesWorkerHistoryUntouched(t *testing.T) {
	f := newFixture(t)
	f.round(t, "job-1", "complete", "")

	_, err := f.svc.Instruct(t.Context(), f.coord, op("denied"), workerops.Instruction{
		Worker: "surveyor", Instructions: "Edit the source", AgentType: "implementer", Brief: "Update the implementation", AskMode: true,
	}, f.enqueue("job-2"))
	requireAskWorkerRefusal(t, err)

	history, err := f.records.History(t.Context(), f.coord, f.workerID, 0, 10)
	testutil.FailErr(t, "read worker history after refusal", err)
	if len(history) != 0 || len(f.enqueued) != 0 || len(f.released) != 0 {
		t.Fatalf("refusal changed history or queued work: history = %v, jobs = %v, released = %v", history, f.enqueued, f.released)
	}
	if view := f.state(t); view.State != workerstate.Delivered || view.Reason != workerstate.ReasonUnaccounted {
		t.Fatalf("refusal changed the previous result: %+v", view)
	}

	receipt, err := f.svc.Instruct(t.Context(), f.coord, op("allowed"), workerops.Instruction{
		Worker: "surveyor", Instructions: "Inspect the source", AskMode: true,
	}, f.enqueue("job-2"))
	testutil.FailErr(t, "instruct read-scoped worker", err)
	if receipt.Outcome != workerops.OutcomeStarted || receipt.Round != 2 || receipt.JobID != "job-2" || len(f.enqueued) != 1 {
		t.Fatalf("read instruction receipt = %+v, jobs = %v", receipt, f.enqueued)
	}
}

func requireAskWorkerRefusal(t *testing.T, err error) {
	t.Helper()
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != tools.AskModeActiveCode {
		t.Fatalf("error = %v, want Ask mode refusal", err)
	}
}
