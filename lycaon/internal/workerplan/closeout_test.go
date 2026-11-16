package workerplan_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerplan"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

func (e *planEnv) closeout(t *testing.T) workerplan.CloseoutVerdict {
	t.Helper()
	verdict, err := e.svc.CheckSessionCloseout(t.Context(), e.coord.SessionID, "/repo")
	testutil.FailErr(t, "closeout", err)
	complete, err := e.svc.CloseoutComplete(t.Context(), e.coord.SessionID)
	testutil.FailErr(t, "closeout complete", err)
	if complete != verdict.OK {
		t.Fatalf("CloseoutComplete = %v while the verdict is %+v", complete, verdict)
	}
	return verdict
}

func wantGap(t *testing.T, verdict workerplan.CloseoutVerdict, gap workerplan.Gap, resultID string) {
	t.Helper()
	if verdict.OK || verdict.Code != workerplan.CodeCriteriaUnmet || verdict.Gap != gap ||
		verdict.LegID != "leg-close" || verdict.Worker != "impl-close" || verdict.ResultID != resultID {
		t.Fatalf("verdict = %+v, want gap %q on result %q", verdict, gap, resultID)
	}
}

// A planned worker blocks closeout until it ran, delivered, met its criteria
// and had its changes landed; each step that is missing is named.
func TestCloseoutNamesWhatAPlannedWorkerStillOwes(t *testing.T) {
	env := newPlanEnv(t)
	env.workflowRun(t, "run-close", "work")
	const fileCriterion = "file:modified:pkg/close.go"
	w := env.worker(t, workerrecords.WorkerSpec{
		Name: "impl-close", WorkflowRunID: "run-close", WorkflowPhase: "work", WorkflowWorkID: "leg-close",
		BlueprintPath: ".lycaon/plans/closeout.md", Files: []string{"pkg/close.go"},
		Criteria: []string{"job:complete", "summary:present", fileCriterion},
	})
	settledRun := func() string {
		runID, err := env.svc.SettledWorkflowRunID(t.Context(), env.coord.SessionID)
		testutil.FailErr(t, "settled run", err)
		return runID
	}

	wantGap(t, env.closeout(t), workerplan.GapUnstarted, "")

	env.startRound(t, w.ID, "job-1")
	wantGap(t, env.closeout(t), workerplan.GapRoundActive, "")

	first := env.settleRound(t, "job-1")
	env.recordOutcomes(t, first,
		workerrecords.CriterionOutcome{Criterion: "job:complete", Passed: true},
		workerrecords.CriterionOutcome{Criterion: "summary:present", Passed: true},
		workerrecords.CriterionOutcome{Criterion: fileCriterion, Detail: "file_unmodified"},
	)
	verdict := env.closeout(t)
	wantGap(t, verdict, workerplan.GapCriteriaUnmet, first)
	if !slices.Equal(verdict.Unmet, []string{fileCriterion}) || len(verdict.Outcomes) != 3 {
		t.Fatalf("verdict = %+v, want the file criterion unmet", verdict)
	}

	// A correction round passes; its changes are still on the worker's branch.
	env.startRound(t, w.ID, "job-2")
	second := env.settleRound(t, "job-2")
	env.recordOutcomes(t, second,
		workerrecords.CriterionOutcome{Criterion: "job:complete", Passed: true},
		workerrecords.CriterionOutcome{Criterion: "summary:present", Passed: true},
		workerrecords.CriterionOutcome{Criterion: fileCriterion, Passed: true},
	)
	env.jobs["job-2"] = &api.WorkerTask{ID: "job-2", Status: api.WorkerStatusComplete, MergeStatus: api.WorkerMergeStatusPending}
	wantGap(t, env.closeout(t), workerplan.GapChangesPending, second)

	// Shelving accounts for the changes, but the plan requires the file.
	_, err := env.records.SetChange(t.Context(), env.coord, workerrecords.Op{ID: "shelve"}, second, workerrecords.Change{
		State: workerrecords.ChangeShelved, ShelveReason: "defer",
	})
	testutil.FailErr(t, "shelve", err)
	wantGap(t, env.closeout(t), workerplan.GapChangesDiscarded, second)
	if runID := settledRun(); runID != "" {
		t.Fatalf("settled run = %q before the plan settled", runID)
	}

	_, err = env.db.ExecContext(t.Context(), `INSERT INTO landed_changes (id, worker_job_id, canonical_path, created_at) VALUES ('land-2', 'job-2', '/repo', '2026-11-12T00:00:00Z')`)
	testutil.FailErr(t, "landing receipt", err)
	_, err = env.records.SetChange(t.Context(), env.coord, workerrecords.Op{ID: "land"}, second, workerrecords.Change{
		State: workerrecords.ChangeLanded, LandedChangeID: "land-2",
	})
	testutil.FailErr(t, "land", err)
	env.jobs["job-2"].MergeStatus = api.WorkerMergeStatusMerged
	if verdict := env.closeout(t); !verdict.OK {
		t.Fatalf("verdict = %+v after the changes landed", verdict)
	}
	if runID := settledRun(); runID != "run-close" {
		t.Fatalf("settled run = %q, want run-close", runID)
	}
}

// A result delivered without recorded outcomes is judged against the
// worker's criteria at closeout.
func TestCloseoutEvaluatesCriteriaTheDeliveryDidNotRecord(t *testing.T) {
	env := newPlanEnv(t)
	w := env.worker(t, workerrecords.WorkerSpec{Name: "impl-close", ScopeMode: workerrecords.ScopeRead, Criteria: []string{"job:complete", "summary:present"}})
	env.startRound(t, w.ID, "job-1")
	result := env.settleRound(t, "job-1")
	env.jobs["job-1"] = &api.WorkerTask{ID: "job-1", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{Summary: " "}}

	verdict := env.closeout(t)
	if verdict.Gap != workerplan.GapCriteriaUnmet || verdict.ResultID != result || !slices.Equal(verdict.Unmet, []string{"summary:present"}) {
		t.Fatalf("verdict = %+v, want the summary criterion unmet", verdict)
	}

	env.jobs["job-1"].Result.Summary = "Surveyed the package."
	if verdict := env.closeout(t); !verdict.OK {
		t.Fatalf("verdict = %+v with every criterion met", verdict)
	}
}

func TestCloseoutWaitsForTheScanOfLandedChanges(t *testing.T) {
	env := newPlanEnv(t)
	w := env.worker(t, workerrecords.WorkerSpec{Name: "impl-close", WorkflowWorkID: "leg-close", BlueprintPath: ".lycaon/plans/closeout.md"})
	env.startRound(t, w.ID, "job-1")
	env.settleRound(t, "job-1")

	for _, tc := range []struct {
		gate error
		gap  workerplan.Gap
	}{
		{&scan.GatePendingError{ScanID: "scan-1"}, workerplan.GapScanPending},
		{&scan.GateFailedError{ScanID: "scan-1", Reason: "scan obligation failed"}, workerplan.GapScanFailed},
	} {
		env.security.err = tc.gate
		verdict := env.closeout(t)
		if verdict.OK || verdict.Gap != tc.gap || verdict.ScanID != "scan-1" || verdict.Worker != "impl-close" {
			t.Fatalf("verdict = %+v, want gap %q on scan-1", verdict, tc.gap)
		}
	}

	env.security.err = errors.New("scan ledger offline")
	if _, err := env.svc.CheckSessionCloseout(t.Context(), env.coord.SessionID, "/repo"); !errors.Is(err, env.security.err) {
		t.Fatalf("error = %v, want the scan ledger's", err)
	}

	env.security.err = nil
	if verdict := env.closeout(t); !verdict.OK {
		t.Fatalf("verdict = %+v with a clear scan gate", verdict)
	}
}

// Workers the coordinator started without a plan or criteria never hold
// closeout, and neither do closed planned workers.
func TestOnlyOpenPlannedWorkersHoldCloseout(t *testing.T) {
	env := newPlanEnv(t)
	env.security.err = &scan.GatePendingError{ScanID: "scan-1"}
	if verdict := env.closeout(t); !verdict.OK {
		t.Fatalf("verdict = %+v for a session with no workers", verdict)
	}
	env.worker(t, workerrecords.WorkerSpec{Name: "ad-hoc"})
	withdrawn := env.worker(t, workerrecords.WorkerSpec{Name: "withdrawn", WorkflowWorkID: "leg-x", BlueprintPath: ".lycaon/plans/closeout.md"})
	_, err := env.records.Close(t.Context(), env.coord, workerrecords.Op{ID: "withdraw"}, withdrawn.ID, "withdrawn")
	testutil.FailErr(t, "close", err)
	if verdict := env.closeout(t); !verdict.OK {
		t.Fatalf("verdict = %+v with no open planned worker", verdict)
	}
}
