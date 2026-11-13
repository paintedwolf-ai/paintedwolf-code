package workerops_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerops"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workerstate"
	"github.com/lycaon/lycaon/pkg/api"
)

type jobs map[string]*api.WorkerTask

func (j jobs) Get(id string) (*api.WorkerTask, bool) { t, ok := j[id]; return t, ok }

type decisions map[string]api.WorkerDecisionRequest

func (d decisions) Get(_ context.Context, child string) (api.WorkerDecisionRequest, bool, error) {
	r, ok := d[child]
	return r, ok, nil
}

type fixture struct {
	db       *db.Store
	records  *workerrecords.Store
	jobs     jobs
	dec      decisions
	svc      *workerops.Service
	coord    workerrecords.Actor
	workerID string
	enqueued []workerops.RoundJob
}

func now() string { return db.FormatTime(time.Now().UTC()) }

func newFixture(t *testing.T) *fixture {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "ops.db")
	testdbseed.InsertSession(t, sqlDB, "coord", testdbseed.DefaultProjectID)
	_, err := sqlDB.ExecContext(t.Context(), `INSERT INTO sessions (id, project_id, owner_person_id, posture, status, parent_session_id, agent_type, created_at, activity_at, updated_at)
		VALUES ('child', ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', 'coord', 'repo-researcher', ?, ?, ?)`, testdbseed.DefaultProjectID, now(), now(), now())
	testutil.FailErr(t, "insert child", err)
	records := workerrecords.New(sqlDB)
	f := &fixture{db: sqlDB, records: records, jobs: jobs{}, dec: decisions{},
		coord: workerrecords.Actor{SessionID: "coord", Role: workerrecords.RoleCoordinator, PrincipalID: "coord"}}
	f.svc = &workerops.Service{
		Reader: workerops.Reader{Records: records, Jobs: f.jobs, Decisions: f.dec},
		Agents: func(agent string) (bool, bool) {
			switch agent {
			case "implementer":
				return true, true
			case "repo-researcher":
				return false, true
			}
			return false, false
		},
	}
	w, err := records.CreateWorker(t.Context(), f.coord, workerrecords.Op{ID: "create"}, workerrecords.WorkerSpec{Name: "surveyor", AgentType: "repo-researcher", ScopeMode: "read", ChildSessionID: "child"})
	testutil.FailErr(t, "create worker", err)
	f.workerID = w.WorkerID
	return f
}

func (f *fixture) job(t *testing.T, id string, status api.WorkerStatus, legStatus string) {
	t.Helper()
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO worker_jobs (id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, created_at)
		VALUES (?, ?, 'coord', 'child', 'repo-researcher', ?, 'p', 'b', ?)`, id, testdbseed.DefaultProjectID, string(status), now())
	testutil.FailErr(t, "insert job "+id, err)
	task := &api.WorkerTask{ID: id, Status: status}
	if legStatus != "" {
		task.Result = &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: legStatus}}
	}
	f.jobs[id] = task
}

// round runs one round on jobID to settlement, with a result and optional
// checkpoint.
func (f *fixture) round(t *testing.T, jobID, legStatus, checkpoint string) {
	t.Helper()
	f.job(t, jobID, api.WorkerStatusRunning, "")
	_, err := f.records.AdmitRound(t.Context(), f.coord, workerrecords.Op{ID: "admit-" + jobID}, f.workerID, workerrecords.RoundSpec{JobID: jobID, ExecutionTarget: "local"})
	testutil.FailErr(t, "admit", err)
	f.settle(t, jobID, legStatus, checkpoint)
}

func (f *fixture) settle(t *testing.T, jobID, legStatus, checkpoint string) {
	t.Helper()
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES (?, ?, 'complete', ?)`, "result-"+jobID, jobID, now())
	testutil.FailErr(t, "insert result", err)
	_, err = f.records.SettleRound(t.Context(), f.coord, workerrecords.Op{ID: "settle-" + jobID}, f.workerID, jobID, checkpoint)
	testutil.FailErr(t, "settle", err)
	status := api.WorkerStatusComplete
	if legStatus == "" {
		status = api.WorkerStatusFailed
	}
	f.jobs[jobID] = &api.WorkerTask{ID: jobID, Status: status}
	if legStatus != "" {
		f.jobs[jobID].Result = &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: legStatus}}
	}
}

func (f *fixture) enqueue(next string) workerops.Enqueuer {
	return func(ctx context.Context, job workerops.RoundJob) (string, string, error) {
		f.enqueued = append(f.enqueued, job)
		if _, ok := f.jobs[next]; !ok {
			_, err := f.db.ExecContext(ctx, `INSERT INTO worker_jobs (id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, created_at)
				VALUES (?, ?, 'coord', 'child', ?, 'pending', 'p', 'b', ?)`, next, testdbseed.DefaultProjectID, job.Worker.AgentType, now())
			if err != nil {
				return "", "", err
			}
			f.jobs[next] = &api.WorkerTask{ID: next, Status: api.WorkerStatusPending}
		}
		return next, "local", nil
	}
}

func (f *fixture) state(t *testing.T) workerstate.View {
	t.Helper()
	w, err := f.records.Worker(t.Context(), f.coord, f.workerID)
	testutil.FailErr(t, "worker", err)
	snap, err := f.svc.Reader.Read(t.Context(), f.coord, w)
	testutil.FailErr(t, "read", err)
	return snap.View
}

func op(id string) workerrecords.Op { return workerrecords.Op{ID: id} }

// A surveyor that delivered becomes the implementer with one instruction:
// the same conversation, a new round, its result accounted for.
func TestInstructStartsARoundAndAccountsForTheResult(t *testing.T) {
	f := newFixture(t)
	f.round(t, "job-1", "complete", "")
	if v := f.state(t); v.State != workerstate.Delivered || v.Reason != workerstate.ReasonUnaccounted {
		t.Fatalf("before = %+v", v)
	}
	receipt, err := f.svc.Instruct(t.Context(), f.coord, op("instruct-1"), workerops.Instruction{
		Worker: "surveyor", Instructions: "implement the fix you found", AgentType: "implementer", Brief: "Fix the parser",
	}, f.enqueue("job-2"))
	testutil.FailErr(t, "instruct", err)
	if receipt.Outcome != workerops.OutcomeStarted || receipt.Round != 2 || receipt.JobID != "job-2" || receipt.Accounted != "result-job-1" {
		t.Fatalf("receipt = %+v", receipt)
	}
	if len(f.enqueued) != 1 || f.enqueued[0].ScopeMode != "write" || f.enqueued[0].AgentType != "implementer" {
		t.Fatalf("enqueued = %+v", f.enqueued)
	}
	w, err := f.records.Worker(t.Context(), f.coord, f.workerID)
	testutil.FailErr(t, "worker", err)
	if w.AgentType != "implementer" || w.ScopeMode != "write" || w.Brief != "Fix the parser" || w.ChildSessionID != "child" {
		t.Fatalf("worker = %+v", w)
	}
	if v := f.state(t); v.State != workerstate.Working || v.Reason != workerstate.ReasonQueued {
		t.Fatalf("after = %+v", v)
	}
}

// An instruction to a running round is queued for its next model call and
// starts nothing.
func TestInstructQueuesForARunningRound(t *testing.T) {
	f := newFixture(t)
	f.job(t, "job-1", api.WorkerStatusRunning, "")
	_, err := f.records.AdmitRound(t.Context(), f.coord, op("admit"), f.workerID, workerrecords.RoundSpec{JobID: "job-1", ExecutionTarget: "local"})
	testutil.FailErr(t, "admit", err)
	receipt, err := f.svc.Instruct(t.Context(), f.coord, op("instruct"), workerops.Instruction{Worker: "surveyor", Instructions: "also read the encoder"}, f.enqueue("unused"))
	testutil.FailErr(t, "instruct", err)
	if receipt.Outcome != workerops.OutcomeQueued || receipt.Round != 1 || len(f.enqueued) != 0 {
		t.Fatalf("receipt = %+v enqueued = %d", receipt, len(f.enqueued))
	}
	// An agent change waits for the round to end.
	_, err = f.svc.Instruct(t.Context(), f.coord, op("change"), workerops.Instruction{Worker: "surveyor", Instructions: "implement", AgentType: "implementer"}, f.enqueue("unused"))
	var refusal *workerops.Refusal
	if !errors.As(err, &refusal) || refusal.AgentChange != workerstate.AgentChangeBusy {
		t.Fatalf("err = %v", err)
	}
}

func TestInstructRefusalsFollowState(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*testing.T, *fixture)
		state workerstate.State
	}{
		{"asking", func(t *testing.T, f *fixture) {
			f.job(t, "job-1", api.WorkerStatusRunning, "")
			_, err := f.records.AdmitRound(t.Context(), f.coord, op("admit"), f.workerID, workerrecords.RoundSpec{JobID: "job-1", ExecutionTarget: "local"})
			testutil.FailErr(t, "admit", err)
			f.dec["child"] = api.WorkerDecisionRequest{ChildSessionID: "child", WorkerID: "job-1", Question: "which?"}
		}, workerstate.Asking},
		{"waiting", func(t *testing.T, f *fixture) {
			f.job(t, "job-1", api.WorkerStatusHeld, "")
			_, err := f.records.AdmitRound(t.Context(), f.coord, op("admit"), f.workerID, workerrecords.RoundSpec{JobID: "job-1", ExecutionTarget: "local"})
			testutil.FailErr(t, "admit", err)
		}, workerstate.Waiting},
		{"closed", func(t *testing.T, f *fixture) {
			_, err := f.records.Close(t.Context(), f.coord, op("close"), f.workerID, "withdrawn")
			testutil.FailErr(t, "close", err)
		}, workerstate.Closed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.setup(t, f)
			_, err := f.svc.Instruct(t.Context(), f.coord, op("instruct"), workerops.Instruction{Worker: "surveyor", Instructions: "go"}, f.enqueue("job-x"))
			var refusal *workerops.Refusal
			if tc.state == workerstate.Closed {
				// A closed worker no longer resolves by name for new work.
				if refusal == nil && err == nil {
					t.Fatal("closed worker instructed")
				}
				return
			}
			if !errors.As(err, &refusal) || refusal.View.State != tc.state || len(f.enqueued) != 0 {
				t.Fatalf("err = %v enqueued = %d", err, len(f.enqueued))
			}
		})
	}
}

// A write worker cannot become a read worker while its changes are open,
// shelved ones included.
func TestAgentChangeRefusedWithOpenChanges(t *testing.T) {
	f := newFixture(t)
	f.round(t, "job-1", "complete", "")
	_, err := f.svc.Instruct(t.Context(), f.coord, op("to-write"), workerops.Instruction{Worker: "surveyor", Instructions: "implement", AgentType: "implementer"}, f.enqueue("job-2"))
	testutil.FailErr(t, "to write", err)
	f.settle(t, "job-2", "complete", "checkpoint-2")
	d, err := f.svc.Shelve(t.Context(), f.coord, op("shelve"), "result-job-2", "waiting on the API freeze")
	testutil.FailErr(t, "shelve", err)
	if d.ChangeState != workerrecords.ChangeShelved || d.AccountedAt == "" {
		t.Fatalf("disposition = %+v", d)
	}
	if v := f.state(t); v.State != workerstate.Idle || !v.Admits(workerstate.OpPromote) || v.Admits(workerstate.OpShelve) {
		t.Fatalf("after shelving = %+v", v)
	}
	_, err = f.svc.Instruct(t.Context(), f.coord, op("to-read"), workerops.Instruction{Worker: "surveyor", Instructions: "survey", AgentType: "repo-researcher"}, f.enqueue("job-3"))
	var refusal *workerops.Refusal
	if !errors.As(err, &refusal) || refusal.AgentChange != workerstate.AgentChangeOpenChanges {
		t.Fatalf("err = %v", err)
	}
}

// Shelving names one result; a result that changed nothing has nothing to
// shelve even while a later one does.
func TestShelveNeedsChanges(t *testing.T) {
	f := newFixture(t)
	f.round(t, "job-1", "complete", "")
	_, err := f.svc.Shelve(t.Context(), f.coord, op("shelve-none"), "result-job-1", "later")
	var refusal *workerops.Refusal
	if !errors.As(err, &refusal) || refusal.View.State != workerstate.Delivered {
		t.Fatalf("shelving with nothing open: err = %v", err)
	}
	_, err = f.svc.Instruct(t.Context(), f.coord, op("to-write"), workerops.Instruction{Worker: "surveyor", Instructions: "implement", AgentType: "implementer"}, f.enqueue("job-2"))
	testutil.FailErr(t, "to write", err)
	f.settle(t, "job-2", "complete", "checkpoint-2")
	_, err = f.svc.Shelve(t.Context(), f.coord, op("shelve-1"), "result-job-1", "later")
	if !errors.Is(err, workerops.ErrNoChanges) {
		t.Fatalf("err = %v, want no changes", err)
	}
	_, err = f.svc.Shelve(t.Context(), f.coord, op("shelve-2"), "result-job-2", " ")
	if !errors.Is(err, workerrecords.ErrReasonRequired) {
		t.Fatalf("err = %v", err)
	}
}

// History pages older messages and resolves nothing.
func TestHistoryPagesWithoutResolving(t *testing.T) {
	f := newFixture(t)
	f.job(t, "job-1", api.WorkerStatusRunning, "")
	_, err := f.records.AdmitRound(t.Context(), f.coord, op("admit"), f.workerID, workerrecords.RoundSpec{JobID: "job-1", ExecutionTarget: "local"})
	testutil.FailErr(t, "admit", err)
	for i := range 5 {
		_, err := f.records.AppendMessage(t.Context(), f.coord, op("m"+string(rune('a'+i))), f.workerID, workerrecords.MessageSpec{Body: "note"})
		testutil.FailErr(t, "append", err)
	}
	page, err := f.svc.History(t.Context(), f.coord, "surveyor", 0, 3)
	testutil.FailErr(t, "history", err)
	if len(page.Messages) != 3 || page.Messages[0].Seq != 5 || page.Before != 3 || len(page.Rounds) != 1 {
		t.Fatalf("page = %+v", page)
	}
	older, err := f.svc.History(t.Context(), f.coord, "surveyor", page.Before, 3)
	testutil.FailErr(t, "older", err)
	if len(older.Messages) != 2 || older.Before != 0 {
		t.Fatalf("older = %+v", older)
	}
}

// A replayed instruction returns what it did the first time, though the
// worker is now working on the round it started.
func TestInstructReplaysItsFirstReceipt(t *testing.T) {
	f := newFixture(t)
	f.round(t, "job-1", "complete", "")
	in := workerops.Instruction{Worker: "surveyor", Instructions: "look again"}
	first, err := f.svc.Instruct(t.Context(), f.coord, op("instruct"), in, f.enqueue("job-2"))
	testutil.FailErr(t, "instruct", err)
	again, err := f.svc.Instruct(t.Context(), f.coord, op("instruct"), in, f.enqueue("job-3"))
	testutil.FailErr(t, "replay", err)
	if again != first || len(f.enqueued) != 1 {
		t.Fatalf("replay = %+v, first = %+v, enqueued %d", again, first, len(f.enqueued))
	}
	queued, err := f.svc.Instruct(t.Context(), f.coord, op("queue"), workerops.Instruction{Worker: "surveyor", Instructions: "and the caller"}, f.enqueue("unused"))
	testutil.FailErr(t, "queue", err)
	replayed, err := f.svc.Instruct(t.Context(), f.coord, op("queue"), workerops.Instruction{Worker: "surveyor", Instructions: "and the caller"}, f.enqueue("unused"))
	testutil.FailErr(t, "replay queued", err)
	if replayed != queued || queued.JobID != "job-2" {
		t.Fatalf("queued = %+v, replayed = %+v", queued, replayed)
	}
}

// A worker name resolves to the job of its latest result, and delivered
// workers are counted; a name with no worker, or no result yet, resolves to
// nothing.
func TestLatestResultJobAndDeliveredCount(t *testing.T) {
	f := newFixture(t)
	if _, ok, err := f.svc.Reader.LatestResultJob(t.Context(), f.coord, "surveyor"); ok || err != nil {
		t.Fatalf("before any result: ok=%v err=%v", ok, err)
	}
	if n, err := f.svc.Reader.CountState(t.Context(), f.coord, workerstate.Delivered); n != 0 || err != nil {
		t.Fatalf("delivered before any result = %d, %v", n, err)
	}
	f.round(t, "job-1", "complete", "")
	job, ok, err := f.svc.Reader.LatestResultJob(t.Context(), f.coord, "surveyor")
	if !ok || err != nil || job != "job-1" {
		t.Fatalf("latest result job = %q ok=%v err=%v", job, ok, err)
	}
	if n, err := f.svc.Reader.CountState(t.Context(), f.coord, workerstate.Delivered); n != 1 || err != nil {
		t.Fatalf("delivered = %d, %v", n, err)
	}
	if _, ok, err := f.svc.Reader.LatestResultJob(t.Context(), f.coord, "nobody"); ok || err != nil {
		t.Fatalf("unknown name: ok=%v err=%v", ok, err)
	}
}
