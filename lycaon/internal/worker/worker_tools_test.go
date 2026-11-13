package worker

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	sessiondecisions "github.com/lycaon/lycaon/internal/session/decisions"
	"github.com/lycaon/lycaon/internal/testbaseline"
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
	root      string
	ops       *workerops.Service
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
		root:  t.TempDir(),
	}
	testdbseed.InsertProjectRootWithID(t, database, testdbseed.DefaultProjectID, "root-id", f.root)
	w, err := f.records.CreateWorker(t.Context(), f.coord, workerrecords.Op{ID: "create"}, workerrecords.WorkerSpec{
		Name: "surveyor", AgentType: "repo-researcher", ScopeMode: "read", ChildSessionID: "child",
	})
	testutil.FailErr(t, "create worker", err)
	f.workerID = w.WorkerID
	f.ops = &workerops.Service{
		Reader: workerops.Reader{Records: f.records, Jobs: f.queue, Decisions: f.decisions},
		Agents: prompts.AgentMutationCapable,
	}
	testutil.FailErr(t, "register", RegisterWorkerTools(f.reg, WorkerToolDeps{Ops: f.ops, Queue: f.queue}))
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
		Source:   tools.InvocationSource{ActiveRootID: "root-id", Roots: []projectroot.RootRef{{ID: "root-id", Path: f.root, IsPrimary: true}}},
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
	// The card's next step answers the question it names.
	var reject *toolrejection.ToolReject
	if errors.As(err, &reject) && reject.Data["job_id"] != job {
		t.Fatalf("refusal data = %v, want job_id %s", reject.Data, job)
	}
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

// Shelved changes ask for no promotion: they leave the pending list that
// gates closing the batch and selects the promotion surface.
func TestShelvedChangesLeavePendingPromotion(t *testing.T) {
	f := newWorkerToolsFixture(t)
	testutil.FailErr(t, "insert job", f.queue.store.InsertTask(t.Context(), api.WorkerTask{
		ID: "job-1", ProjectID: testdbseed.DefaultProjectID, ParentSessionID: "coord", ChildSessionID: "child",
		AgentType: "implementer", Status: api.WorkerStatusRunning, ExecutionTarget: api.ExecutionTargetLocal,
		Prompt: "fix", Brief: "fix", Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite}, OverlayID: "job-1",
	}))
	root := filepath.Join(testbaseline.DataDir(t, f.db), "worker-branches", "job-1")
	bound, err := f.queue.store.SetWorkerWorkspace(t.Context(), "job-1", root, testbaseline.Durable(t, f.db, "job-1", t.TempDir()))
	if err != nil || !bound {
		t.Fatalf("bind workspace: bound=%v err=%v", bound, err)
	}
	_, err = f.records.AdmitRound(t.Context(), f.coord, workerrecords.Op{ID: "admit"}, f.workerID, workerrecords.RoundSpec{JobID: "job-1", ExecutionTarget: "local"})
	testutil.FailErr(t, "admit", err)
	now := db.FormatTime(time.Now().UTC())
	report, _ := json.Marshal(api.WorkerResult{Status: "complete", CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}})
	_, err = f.db.ExecContext(t.Context(), `UPDATE worker_jobs SET status = 'complete', merge_status = 'pending', result_json = ?, completed_at = ? WHERE id = 'job-1'`, string(report), now)
	testutil.FailErr(t, "complete", err)
	_, err = f.db.ExecContext(t.Context(), `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES ('result-job-1', 'job-1', 'complete', ?)`, now)
	testutil.FailErr(t, "result", err)
	_, err = f.records.SettleRound(t.Context(), f.coord, workerrecords.Op{ID: "settle"}, f.workerID, "job-1", "checkpoint-1")
	testutil.FailErr(t, "settle", err)

	pending, err := f.queue.ListPendingOverlayPromote(t.Context(), "coord")
	testutil.FailErr(t, "pending before", err)
	if len(pending) != 1 {
		t.Fatalf("pending before shelving = %d", len(pending))
	}
	_, err = f.run(t, ShelveOverlayTool, "call-1", map[string]any{"overlay_id": "result-job-1", "reason": "waits for the API freeze"})
	testutil.FailErr(t, "shelve", err)
	pending, err = f.queue.ListPendingOverlayPromote(t.Context(), "coord")
	testutil.FailErr(t, "pending after", err)
	if len(pending) != 0 {
		t.Fatalf("shelved changes still pending: %+v", pending)
	}
}

// A consumer waits on its producer worker's latest round: sending the
// producer back holds the consumer until the new round delivers.
func TestPrerequisiteFollowsTheProducersLatestRound(t *testing.T) {
	f := newWorkerToolsFixture(t)
	f.deliver(t, "job-1")
	testutil.FailErr(t, "insert consumer", f.queue.store.InsertTask(t.Context(), api.WorkerTask{
		ID: "job-c", ProjectID: testdbseed.DefaultProjectID, ParentSessionID: "coord",
		AgentType: "repo-researcher", Status: api.WorkerStatusPending, ExecutionTarget: api.ExecutionTargetLocal,
		Prompt: "use the survey", Brief: "use the survey", AfterWorkers: []string{"job-1"},
	}))
	next := func() string {
		t.Helper()
		row, err := db.New(f.db).NextPendingWorkerJob(t.Context(), db.NextPendingWorkerJobParams{ExecutionTarget: "local", ProjectID: ""})
		if db.IsNoRows(err) {
			return ""
		}
		testutil.FailErr(t, "next pending", err)
		return row.ID
	}
	if got := next(); got != "job-c" {
		t.Fatalf("consumer of a delivered producer not runnable: next = %q", got)
	}
	_, err := f.run(t, InstructWorkerTool, "call-1", map[string]any{"worker": "surveyor", "instructions": "recheck the encoder"})
	testutil.FailErr(t, "send back", err)
	round2 := latestJob(t, f.db)
	if got := next(); got != round2 {
		t.Fatalf("consumer ran while its producer works: next = %q", got)
	}
	claimed, err := f.queue.ClaimNext(t.Context(), ClaimRequest{ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "claim producer", err)
	won, err := f.queue.Complete(t.Context(), claimed, api.WorkerResult{Status: "complete", CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}})
	if err != nil || !won {
		t.Fatalf("complete producer: won=%v err=%v", won, err)
	}
	if got := next(); got != "job-c" {
		t.Fatalf("consumer still held after its producer delivered again: next = %q", got)
	}
}

// task starts a worker: a named record with the job as its first round,
// bound to the child session the job spawns. A replay starts nothing new and
// a name in use is refused before anything is enqueued.
func TestTaskStartsANamedWorker(t *testing.T) {
	f := newWorkerToolsFixture(t)
	testutil.FailErr(t, "register task", RegisterTaskTool(f.reg, TaskToolDeps{
		Queue: f.queue, Agents: orchestration.NewMemoryAgentRegistryForTest(), Workers: DefaultWorkersConfig(), WorkerRecords: f.records,
	}))
	args := map[string]any{"agent_type": "repo-researcher", "name": "scout", "scope": map[string]any{"mode": "read"},
		"brief": map[string]any{"goal": "Map the parser", "done_when": []any{"Entry points listed"}}}
	out, err := f.run(t, "task", "call-1", args)
	testutil.FailErr(t, "task", err)
	var started struct {
		Worker string `json:"worker"`
		JobID  string `json:"job_id"`
	}
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(out), &started))
	if started.Worker != "scout" || started.JobID == "" {
		t.Fatalf("task output = %s", out)
	}
	again, err := f.run(t, "task", "call-1", args)
	testutil.FailErr(t, "replay", err)
	if again != out || countJobs(t, f.db) != 1 {
		t.Fatalf("replay = %s (jobs %d)", again, countJobs(t, f.db))
	}
	w, err := f.records.Resolve(t.Context(), f.coord, "scout")
	testutil.FailErr(t, "resolve", err)
	rounds, err := f.records.Rounds(t.Context(), f.coord, w.ID)
	testutil.FailErr(t, "rounds", err)
	if len(rounds) != 1 || rounds[0].JobID != started.JobID || w.Brief != "Map the parser" || w.ScopeMode != "read" {
		t.Fatalf("worker = %+v rounds = %+v", w, rounds)
	}

	now := db.FormatTime(time.Now().UTC())
	_, err = f.db.ExecContext(t.Context(), `INSERT INTO sessions (id, project_id, owner_person_id, posture, status, parent_session_id, agent_type, created_at, activity_at, updated_at)
		VALUES ('scout-child', ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', 'coord', 'repo-researcher', ?, ?, ?)`, testdbseed.DefaultProjectID, now, now, now)
	testutil.FailErr(t, "insert child", err)
	testutil.FailErr(t, "spawn child", f.queue.store.SetChildSessionID(t.Context(), started.JobID, "scout-child"))
	w, err = f.records.Resolve(t.Context(), f.coord, "scout")
	testutil.FailErr(t, "resolve again", err)
	if w.ChildSessionID != "scout-child" {
		t.Fatalf("worker child = %q", w.ChildSessionID)
	}

	_, err = f.run(t, "task", "call-2", args)
	wantReject(t, err, TaskWorkerNameTakenCode)
	if countJobs(t, f.db) != 1 {
		t.Fatal("a refused name enqueued a job")
	}
}

// A question is answered by naming the asking worker, in free text when it
// offered no options; a worker that is not asking has nothing to answer.
func TestAnswerDecisionByWorkerName(t *testing.T) {
	f := newWorkerToolsFixture(t)
	resolver := &answerStubResolver{decisions: f.decisions}
	answers := &AnswerDecisionService{Queue: f.queue, Decisions: f.decisions, Resolver: resolver}
	testutil.FailErr(t, "register", RegisterAnswerDecisionTool(f.reg, AnswerDecisionToolDeps{Answer: answers, Workers: f.ops}))
	f.deliver(t, "job-1")
	_, err := f.run(t, "answer_decision", "call-0", map[string]any{"worker": "surveyor", "answer": "utf-8"})
	wantReject(t, err, DecisionNotFoundCode)

	_, err = f.run(t, InstructWorkerTool, "call-1", map[string]any{"worker": "surveyor", "instructions": "check the encoder"})
	testutil.FailErr(t, "instruct", err)
	job := latestJob(t, f.db)
	testutil.FailErr(t, "ask", f.decisions.Put(t.Context(), api.WorkerDecisionRequest{ChildSessionID: "child", WorkerID: job, Question: "Which encoding does the caller expect?"}))
	out, err := f.run(t, "answer_decision", "call-2", map[string]any{"worker": "surveyor", "answer": "UTF-8 without a byte-order mark"})
	testutil.FailErr(t, "answer", err)
	if !strings.Contains(out, `"worker":"surveyor"`) || len(resolver.appended) != 1 || !strings.Contains(resolver.appended[0].Content, "UTF-8 without a byte-order mark") {
		t.Fatalf("answer = %s, appended = %+v", out, resolver.appended)
	}
}

// Stopping names the worker: a working round is stopped, and a worker with
// nothing running is refused.
func TestWorkerCancelByName(t *testing.T) {
	f := newWorkerToolsFixture(t)
	testutil.FailErr(t, "register", RegisterWorkerCancelTool(f.reg, CancelToolDeps{Cancel: &CancelService{Queue: f.queue}, Workers: f.ops}))
	f.deliver(t, "job-1")
	_, err := f.run(t, "worker_cancel", "call-0", map[string]any{"worker": "surveyor"})
	wantReject(t, err, "WORKER_CANCEL_NOT_RUNNING")
}
