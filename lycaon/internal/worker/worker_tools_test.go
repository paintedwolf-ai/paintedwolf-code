package worker

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/prompts"
	sessiondecisions "github.com/lycaon/lycaon/internal/session/decisions"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerops"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

type workerToolsFixture struct {
	db        *db.Store
	queue     *SQLQueue
	records   *workerrecords.Store
	decisions sessiondecisions.Store
	reg       *tools.DefaultRegistry
	coord     workerrecords.Actor
	workerID  string
}

func newWorkerToolsFixture(t *testing.T) *workerToolsFixture {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "coord", testdbseed.DefaultProjectID)
	now := db.FormatTime(time.Now().UTC())
	_, err := database.ExecContext(t.Context(), `INSERT INTO sessions (id, project_id, owner_person_id, posture, status, parent_session_id, agent_type, created_at, activity_at, updated_at)
		VALUES ('child', ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', 'coord', 'repo-researcher', ?, ?, ?)`, testdbseed.DefaultProjectID, now, now, now)
	testutil.FailErr(t, "insert child", err)
	f := &workerToolsFixture{
		db: database, queue: NewSQLQueue(database, 1), records: workerrecords.New(database),
		decisions: sessiondecisions.NewSQL(database), reg: tools.NewDefaultRegistry(),
		coord: workerrecords.Actor{SessionID: "coord", Role: workerrecords.RoleCoordinator, PrincipalID: "coord"},
	}
	w, err := f.records.CreateWorker(t.Context(), f.coord, workerrecords.Op{ID: "create"}, workerrecords.WorkerSpec{
		Name: "surveyor", AgentType: "repo-researcher", ScopeMode: "read", ChildSessionID: "child",
	})
	testutil.FailErr(t, "create worker", err)
	f.workerID = w.WorkerID
	testutil.FailErr(t, "register", RegisterWorkerTools(f.reg, WorkerToolDeps{
		Ops: &workerops.Service{
			Reader: workerops.Reader{Records: f.records, Jobs: f.queue, Decisions: f.decisions},
			Agents: prompts.AgentMutationCapable,
		},
		Queue: f.queue,
	}))
	return f
}

// deliver runs one round on jobID to a complete report.
func (f *workerToolsFixture) deliver(t *testing.T, jobID string) { f.deliverWith(t, jobID, "") }

// deliverWith delivers a round whose changes published checkpoint; a
// non-empty checkpoint leaves its changes pending.
func (f *workerToolsFixture) deliverWith(t *testing.T, jobID, checkpoint string) {
	t.Helper()
	testutil.FailErr(t, "insert job", f.queue.store.InsertTask(t.Context(), api.WorkerTask{
		ID: jobID, ProjectID: testdbseed.DefaultProjectID, ParentSessionID: "coord", ChildSessionID: "child",
		AgentType: "repo-researcher", Status: api.WorkerStatusRunning, ExecutionTarget: api.ExecutionTargetLocal,
		Prompt: "survey", Brief: "survey",
	}))
	_, err := f.records.AdmitRound(t.Context(), f.coord, workerrecords.Op{ID: "admit-" + jobID}, f.workerID, workerrecords.RoundSpec{JobID: jobID, ExecutionTarget: "local"})
	testutil.FailErr(t, "admit", err)
	report, _ := json.Marshal(api.WorkerResult{Status: "complete", CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}})
	now := db.FormatTime(time.Now().UTC())
	_, err = f.db.ExecContext(t.Context(), `UPDATE worker_jobs SET status = 'complete', result_json = ?, completed_at = ? WHERE id = ?`, string(report), now, jobID)
	testutil.FailErr(t, "complete job", err)
	_, err = f.db.ExecContext(t.Context(), `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES (?, ?, 'complete', ?)`, "result-"+jobID, jobID, now)
	testutil.FailErr(t, "insert result", err)
	if checkpoint != "" {
		_, err = f.db.ExecContext(t.Context(), `UPDATE worker_jobs SET merge_status = 'pending' WHERE id = ?`, jobID)
		testutil.FailErr(t, "pending changes", err)
	}
	_, err = f.records.SettleRound(t.Context(), f.coord, workerrecords.Op{ID: "settle-" + jobID}, f.workerID, jobID, checkpoint)
	testutil.FailErr(t, "settle", err)
}

func (f *workerToolsFixture) run(t *testing.T, tool, call string, args map[string]any) (string, error) {
	t.Helper()
	return f.reg.Run(t.Context(), tool, args, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "coord", ProjectID: testdbseed.DefaultProjectID, ToolCallID: call},
	})
}

// One instruction turns the delivered surveyor into the implementer on the
// same child session; replaying the call enqueues nothing new.
func TestInstructWorkerStartsTheNextRoundOnce(t *testing.T) {
	f := newWorkerToolsFixture(t)
	f.deliver(t, "job-1")
	args := map[string]any{"worker": "surveyor", "instructions": "implement the fix you found", "agent_type": "implementer"}
	out, err := f.run(t, InstructWorkerTool, "call-1", args)
	testutil.FailErr(t, "instruct", err)
	var receipt workerops.Receipt
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(out), &receipt))
	if receipt.Outcome != workerops.OutcomeStarted || receipt.Round != 2 || receipt.Accounted != "result-job-1" {
		t.Fatalf("receipt = %s", out)
	}
	job, ok := f.queue.Get(receipt.JobID)
	if !ok || job.ChildSessionID != "child" || job.AgentType != "implementer" || job.EffectiveScope().Mode != api.TaskScopeModeWrite || job.Prompt != "implement the fix you found" {
		t.Fatalf("job = %+v", job)
	}
	again, err := f.run(t, InstructWorkerTool, "call-1", args)
	testutil.FailErr(t, "replay", err)
	if again != out {
		t.Fatalf("replay = %s, first = %s", again, out)
	}
	if n := countJobs(t, f.db); n != 2 {
		t.Fatalf("jobs = %d, want 2", n)
	}
}

func countJobs(t *testing.T, database *db.Store) int {
	t.Helper()
	var n int
	testutil.FailErr(t, "count", database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM worker_jobs`).Scan(&n))
	return n
}

func TestWorkerToolRejections(t *testing.T) {
	f := newWorkerToolsFixture(t)
	_, err := f.run(t, InstructWorkerTool, "call-1", map[string]any{"worker": "nobody", "instructions": "go"})
	wantReject(t, err, "WORKER_NAME_UNKNOWN")
	f.deliver(t, "job-1")
	_, err = f.run(t, InstructWorkerTool, "call-2", map[string]any{"worker": "surveyor", "instructions": "go", "agent_type": "no-such-agent"})
	wantReject(t, err, "WORKER_TYPE_UNAVAILABLE")
	_, err = f.run(t, ShelveOverlayTool, "call-3", map[string]any{"overlay_id": "result-job-1", "reason": "later"})
	wantReject(t, err, "SHELVE_OVERLAY_NOT_ADMITTED")
	_, err = f.run(t, ShelveOverlayTool, "call-4", map[string]any{"overlay_id": "result-missing", "reason": "later"})
	wantReject(t, err, "RESULT_HANDLE_UNKNOWN")

	_, err = f.run(t, InstructWorkerTool, "call-5", map[string]any{"worker": "surveyor", "instructions": "keep going"})
	testutil.FailErr(t, "start round", err)
	job := latestJob(t, f.db)
	testutil.FailErr(t, "put decision", f.decisions.Put(t.Context(), api.WorkerDecisionRequest{ChildSessionID: "child", WorkerID: job, Question: "which?"}))
	_, err = f.run(t, InstructWorkerTool, "call-6", map[string]any{"worker": "surveyor", "instructions": "also this"})
	wantReject(t, err, "INSTRUCT_WORKER_ASKING")
}

func latestJob(t *testing.T, database *db.Store) string {
	t.Helper()
	var id string
	testutil.FailErr(t, "latest job", database.QueryRowContext(t.Context(), `SELECT job_id FROM worker_rounds ORDER BY ordinal DESC LIMIT 1`).Scan(&id))
	return id
}

func wantReject(t *testing.T, err error, code string) {
	t.Helper()
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
}

func TestWorkerHistoryReadsRoundsAndMessages(t *testing.T) {
	f := newWorkerToolsFixture(t)
	f.deliver(t, "job-1")
	_, err := f.run(t, InstructWorkerTool, "call-1", map[string]any{"worker": "surveyor", "instructions": "look at the encoder"})
	testutil.FailErr(t, "instruct", err)
	out, err := f.run(t, WorkerHistoryTool, "call-2", map[string]any{"worker": "surveyor"})
	testutil.FailErr(t, "history", err)
	var page struct {
		State    string           `json:"state"`
		Rounds   []map[string]any `json:"rounds"`
		Messages []map[string]any `json:"messages"`
	}
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(out), &page))
	if page.State != "working" || len(page.Rounds) != 2 || page.Rounds[0]["overlay_id"] != "result-job-1" ||
		len(page.Messages) != 1 || page.Messages[0]["text"] != "look at the encoder" || page.Messages[0]["from"] != "coordinator" {
		t.Fatalf("history = %s", out)
	}
}

// The job's result and its round's settlement commit together: a finished
// instruction round leaves the worker delivered, a failed one failed.
func TestJobOutcomeSettlesItsRound(t *testing.T) {
	for _, tc := range []struct {
		name  string
		end   func(t *testing.T, f *workerToolsFixture, claimed *api.WorkerTask)
		state string
	}{
		{"complete", func(t *testing.T, f *workerToolsFixture, claimed *api.WorkerTask) {
			won, err := f.queue.Complete(t.Context(), claimed, api.WorkerResult{Status: "complete", CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}})
			if err != nil || !won {
				t.Fatalf("complete: won=%v err=%v", won, err)
			}
		}, "delivered"},
		{"failed", func(t *testing.T, f *workerToolsFixture, claimed *api.WorkerTask) {
			won, err := f.queue.Fail(t.Context(), claimed, errors.New("provider gone"))
			if err != nil || !won {
				t.Fatalf("fail: won=%v err=%v", won, err)
			}
		}, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWorkerToolsFixture(t)
			f.deliver(t, "job-1")
			_, err := f.run(t, InstructWorkerTool, "call-1", map[string]any{"worker": "surveyor", "instructions": "check the encoder"})
			testutil.FailErr(t, "instruct", err)
			claimed, err := f.queue.ClaimNext(t.Context(), ClaimRequest{ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal})
			testutil.FailErr(t, "claim", err)
			if claimed == nil || claimed.ID != latestJob(t, f.db) {
				t.Fatalf("claimed %+v", claimed)
			}
			tc.end(t, f, claimed)
			out, err := f.run(t, WorkerHistoryTool, "call-2", map[string]any{"worker": "surveyor"})
			testutil.FailErr(t, "history", err)
			var page struct {
				State string `json:"state"`
			}
			testutil.FailErr(t, "decode", json.Unmarshal([]byte(out), &page))
			if page.State != tc.state {
				t.Fatalf("state = %s, want %s (%s)", page.State, tc.state, out)
			}
		})
	}
}

// Rejecting a result's changes records the rejection and accounts for it in
// the transaction that rejects them; a stacked result orphaned with it loses
// its changes without being accounted for.
func TestRejectionDisposesTheResult(t *testing.T) {
	f := newWorkerToolsFixture(t)
	f.deliverWith(t, "job-1", "checkpoint-1")
	testutil.FailErr(t, "reject", f.queue.store.SetMergeStatuses(t.Context(), []MergeStatusUpdate{{JobID: "job-1", Status: api.WorkerMergeStatusRejected}}))
	ds, err := f.records.Dispositions(t.Context(), f.coord, f.workerID)
	testutil.FailErr(t, "dispositions", err)
	if len(ds) != 1 || ds[0].ChangeState != workerrecords.ChangeRejected || ds[0].AccountedAt == "" {
		t.Fatalf("dispositions = %+v", ds)
	}
	out, err := f.run(t, WorkerHistoryTool, "call-1", map[string]any{"worker": "surveyor"})
	testutil.FailErr(t, "history", err)
	var page struct {
		State string `json:"state"`
	}
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(out), &page))
	if page.State != "idle" {
		t.Fatalf("after rejection: %s", out)
	}
}
