package worker

import (
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/timelayout"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

func (f *workerToolsFixture) changeStates(t *testing.T) map[string]workerrecords.Disposition {
	t.Helper()
	ds, err := f.records.Dispositions(t.Context(), f.coord, f.workerID)
	testutil.FailErr(t, "dispositions", err)
	out := map[string]workerrecords.Disposition{}
	for _, d := range ds {
		out[d.ResultID] = d
	}
	return out
}

// A worker's later rounds carry an earlier round's changes in its lane, so
// rejecting the earlier result rejects the later ones; while a later round
// still runs, the rejection is refused.
func TestRejectionReachesLaterRoundsOfTheLane(t *testing.T) {
	f := newWorkerToolsFixture(t)
	merge := &MergeService{Queue: f.queue, Branches: f.queue.Branches, Store: f.promotions, DataDir: t.TempDir()}
	f.deliverWith(t, "job-1", "checkpoint-1")
	f.deliverWith(t, "job-2", "checkpoint-2")
	f.deliverWith(t, "job-3", "")

	testutil.FailErr(t, "insert running round", f.queue.store.InsertTask(t.Context(), api.WorkerTask{
		ID: "job-4", ProjectID: testdbseed.DefaultProjectID, ParentSessionID: "coord", ChildSessionID: "child",
		AgentType: "repo-researcher", Status: api.WorkerStatusRunning, ExecutionTarget: api.ExecutionTargetLocal,
		Prompt: "survey", Brief: "survey",
	}))
	_, err := f.records.AdmitRound(t.Context(), f.coord, workerrecords.Op{ID: "admit-job-4"}, f.workerID, workerrecords.RoundSpec{JobID: "job-4", ExecutionTarget: "local"})
	testutil.FailErr(t, "admit running round", err)
	_, err = merge.RejectOverlay(t.Context(), "coord", "job-1", "wrong approach")
	wantReject(t, err, OverlayRejectWorkerWorkingCode)
	if states := f.changeStates(t); len(states) != 0 {
		t.Fatalf("refused rejection disposed results: %+v", states)
	}

	n, err := db.New(f.db).SettleWorkerRoundByJob(t.Context(), db.SettleWorkerRoundByJobParams{SettledAt: db.NullString(timelayout.Format(time.Now().UTC())), JobID: "job-4"})
	if err != nil || n != 1 {
		t.Fatalf("settle running round: n=%d err=%v", n, err)
	}
	out, err := merge.RejectOverlay(t.Context(), "coord", "job-1", "wrong approach")
	testutil.FailErr(t, "reject", err)
	if len(out.Later) != 1 || out.Later[0] != "job-2" {
		t.Fatalf("later rounds rejected = %v", out.Later)
	}
	states := f.changeStates(t)
	for _, id := range []string{"result-job-1", "result-job-2"} {
		if states[id].ChangeState != workerrecords.ChangeRejected || states[id].AccountedAt == "" {
			t.Fatalf("%s = %+v", id, states[id])
		}
	}
	if task, ok := f.queue.Get("job-2"); !ok || task.MergeStatus != api.WorkerMergeStatusRejected {
		t.Fatalf("job-2 = %+v", task)
	}
}

// Shelving a result sets aside the earlier pending changes its lane carried.
func TestShelvingCarriesEarlierPendingChanges(t *testing.T) {
	f := newWorkerToolsFixture(t)
	f.deliverWith(t, "job-1", "checkpoint-1")
	f.deliverWith(t, "job-2", "")
	f.deliverWith(t, "job-3", "checkpoint-3")
	_, err := f.run(t, ShelveOverlayTool, "call-shelve", map[string]any{"overlay_id": "result-job-3", "reason": "later"})
	testutil.FailErr(t, "shelve", err)
	states := f.changeStates(t)
	for _, id := range []string{"result-job-1", "result-job-3"} {
		if states[id].ChangeState != workerrecords.ChangeShelved || states[id].AccountedAt == "" {
			t.Fatalf("%s = %+v", id, states[id])
		}
	}
	if _, ok := states["result-job-2"]; ok {
		t.Fatalf("a round without changes was shelved: %+v", states["result-job-2"])
	}
}

func (f *workerToolsFixture) claimRound(t *testing.T, jobID string) *api.WorkerTask {
	t.Helper()
	testutil.FailErr(t, "insert job", f.queue.store.InsertTask(t.Context(), api.WorkerTask{
		ID: jobID, ProjectID: testdbseed.DefaultProjectID, ParentSessionID: "coord", ChildSessionID: "child",
		AgentType: "implementer", Status: api.WorkerStatusPending, ExecutionTarget: api.ExecutionTargetLocal,
		Prompt: "fix", Brief: "fix", Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite},
	}))
	_, err := f.records.AdmitRound(t.Context(), f.coord, workerrecords.Op{ID: "admit-" + jobID}, f.workerID, workerrecords.RoundSpec{JobID: jobID, ExecutionTarget: "local"})
	testutil.FailErr(t, "admit", err)
	claimed, err := f.queue.store.ClaimNext(t.Context(), ClaimRequest{ProjectID: testdbseed.DefaultProjectID, ExecutionTarget: api.ExecutionTargetLocal}, 0, nil)
	testutil.FailErr(t, "claim", err)
	if claimed == nil || claimed.ID != jobID {
		t.Fatalf("claimed %+v, want %s", claimed, jobID)
	}
	return claimed
}

// A write round that fails or is stopped keeps the changes its branch
// published under its result, which then admits the change tools; one
// without changes is aborted.
func TestEndedRoundsKeepTheirChanges(t *testing.T) {
	f := newWorkerToolsFixture(t)
	claimed := f.claimRound(t, "job-1")
	won, err := f.queue.store.FailJob(t.Context(), claimed, "limit", nil, "checkpoint-1", "")
	if err != nil || !won {
		t.Fatalf("fail: won=%v err=%v", won, err)
	}
	if task, ok := f.queue.Get("job-1"); !ok || task.MergeStatus != api.WorkerMergeStatusPending {
		t.Fatalf("failed round = %+v", task)
	}
	var resultID string
	testutil.FailErr(t, "read result", f.db.QueryRowContext(t.Context(), `SELECT id FROM worker_results WHERE worker_job_id = 'job-1'`).Scan(&resultID))
	_, err = f.run(t, ShelveOverlayTool, "call-shelve", map[string]any{"overlay_id": resultID, "reason": "later"})
	testutil.FailErr(t, "shelve the failed round's changes", err)

	f.claimRound(t, "job-2")
	won, err = f.queue.store.CancelJob(t.Context(), "job-2", nil, "checkpoint-2")
	if err != nil || !won {
		t.Fatalf("cancel: won=%v err=%v", won, err)
	}
	rounds, err := f.records.Rounds(t.Context(), f.coord, f.workerID)
	testutil.FailErr(t, "rounds", err)
	if len(rounds) != 2 || rounds[0].CheckpointID != "checkpoint-1" || rounds[1].CheckpointID != "checkpoint-2" {
		t.Fatalf("rounds = %+v", rounds)
	}
	if task, ok := f.queue.Get("job-2"); !ok || task.MergeStatus != api.WorkerMergeStatusPending {
		t.Fatalf("stopped round = %+v", task)
	}

	claimed = f.claimRound(t, "job-3")
	won, err = f.queue.store.FailJob(t.Context(), claimed, "error", nil, "", "")
	if err != nil || !won {
		t.Fatalf("fail without changes: won=%v err=%v", won, err)
	}
	if task, ok := f.queue.Get("job-3"); !ok || task.MergeStatus != api.WorkerMergeStatusAborted {
		t.Fatalf("round without changes = %+v", task)
	}
}
