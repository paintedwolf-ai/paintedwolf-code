package workerrecords_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerrecords"
)

func wantErr(t *testing.T, label string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: err = %v, want %v", label, err, want)
	}
}

func TestCreateWorkerNamesAndBindings(t *testing.T) {
	f := newFixture(t)
	a := f.createWorker(t, "op-a", workerrecords.WorkerSpec{})
	b := f.createWorker(t, "op-b", workerrecords.WorkerSpec{})
	if a.Name != "implementer-1" || b.Name != "implementer-2" {
		t.Fatalf("derived names = %q, %q", a.Name, b.Name)
	}
	named := f.createWorker(t, "op-c", workerrecords.WorkerSpec{Name: "parser fix"})
	got, err := f.store.Resolve(t.Context(), f.coord, "parser fix")
	testutil.FailErr(t, "resolve", err)
	if got.ID != named.WorkerID || got.SessionID != coordSession || got.ProjectID != testdbseed.DefaultProjectID {
		t.Fatalf("resolved = %+v", got)
	}
	_, err = f.store.CreateWorker(t.Context(), f.coord, op("op-d"), workerrecords.WorkerSpec{Name: "parser fix", AgentType: "implementer", ScopeMode: "write"})
	wantErr(t, "duplicate name", err, workerrecords.ErrNameTaken)
	_, err = f.store.CreateWorker(t.Context(), f.coord, op("op-e"), workerrecords.WorkerSpec{AgentType: "implementer", ScopeMode: "edit"})
	wantErr(t, "scope", err, workerrecords.ErrInvalidScope)

	// Names are unique per session, not globally.
	_, err = f.store.CreateWorker(t.Context(), f.other, op("op-f"), workerrecords.WorkerSpec{Name: "parser fix", AgentType: "implementer", ScopeMode: "write"})
	testutil.FailErr(t, "same name in another session", err)

	testdbseed.InsertWorkflowRun(t, f.db, "run-other", "run-owner", testdbseed.DefaultProjectID)
	_, err = f.store.CreateWorker(t.Context(), f.coord, op("op-g"), workerrecords.WorkerSpec{AgentType: "implementer", ScopeMode: "read", WorkflowRunID: "run-other", WorkflowPhase: "execute", WorkflowWorkID: "leg-1"})
	wantErr(t, "workflow run of another session", err, workerrecords.ErrBindingInvalid)

	insertSession(t, f.db, "foreign-child", otherSession)
	_, err = f.store.CreateWorker(t.Context(), f.coord, op("op-h"), workerrecords.WorkerSpec{AgentType: "implementer", ScopeMode: "read", ChildSessionID: "foreign-child"})
	wantErr(t, "child of another session", err, workerrecords.ErrBindingInvalid)
}

func TestWorkflowWorkBindsOneWorker(t *testing.T) {
	f := newFixture(t)
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO workflow_runs (id, session_id, project_id, workflow_id, workflow_version, status, revision, current_phase, created_at, updated_at)
		VALUES ('run-1', ?, ?, 'wf', '1.0.0', 'running', 1, 'execute', ?, ?)`, coordSession, testdbseed.DefaultProjectID, stamp(), stamp())
	testutil.FailErr(t, "insert run", err)
	spec := workerrecords.WorkerSpec{AgentType: "implementer", ScopeMode: "write", WorkflowRunID: "run-1", WorkflowPhase: "execute", WorkflowWorkID: "leg-1"}
	f.createWorker(t, "bind-1", spec)
	_, err = f.store.CreateWorker(t.Context(), f.coord, op("bind-2"), spec)
	wantErr(t, "second worker on the same work", err, workerrecords.ErrBindingTaken)
	spec.WorkflowPhase = "review"
	f.createWorker(t, "bind-3", spec)
}

func TestOperationReplayAndMismatch(t *testing.T) {
	f := newFixture(t)
	spec := workerrecords.WorkerSpec{Name: "scout", AgentType: "repo-researcher", ScopeMode: "read"}
	first, err := f.store.CreateWorker(t.Context(), f.coord, op("op-1"), spec)
	testutil.FailErr(t, "create", err)
	// A lost response is retried with the same identity: same receipt, no new row.
	again, err := f.store.CreateWorker(t.Context(), f.coord, op("op-1"), spec)
	testutil.FailErr(t, "replay", err)
	if again != first {
		t.Fatalf("replay receipt = %+v, want %+v", again, first)
	}
	if n := countRows(t, f.db, `SELECT COUNT(*) FROM workers WHERE session_id = ?`, coordSession); n != 1 {
		t.Fatalf("workers after replay = %d", n)
	}
	spec.Brief = "different"
	_, err = f.store.CreateWorker(t.Context(), f.coord, op("op-1"), spec)
	wantErr(t, "different content", err, workerrecords.ErrOperationConflict)
	_, err = f.store.Close(t.Context(), f.coord, op("op-1"), first.WorkerID, "withdrawn")
	wantErr(t, "different kind", err, workerrecords.ErrOperationConflict)
	_, err = f.store.CreateWorker(t.Context(), f.other, op("op-1"), workerrecords.WorkerSpec{Name: "scout", AgentType: "repo-researcher", ScopeMode: "read"})
	wantErr(t, "another session", err, workerrecords.ErrOperationConflict)
	_, err = f.store.CreateWorker(t.Context(), f.coord, op(" "), spec)
	wantErr(t, "missing identity", err, workerrecords.ErrOperationRequired)
}

// The hook commits with the state: a failing hook leaves nothing behind, and
// the same operation can then run.
func TestHookCommitsWithStateOrNothing(t *testing.T) {
	f := newFixture(t)
	failing := workerrecords.Op{ID: "op-hook", Hook: func(context.Context, *sql.Tx, any) error { return errors.New("outbox unavailable") }}
	spec := workerrecords.WorkerSpec{Name: "w", AgentType: "implementer", ScopeMode: "write"}
	_, err := f.store.CreateWorker(t.Context(), f.coord, failing, spec)
	if err == nil {
		t.Fatal("hook failure ignored")
	}
	if n := countRows(t, f.db, `SELECT COUNT(*) FROM workers`); n != 0 {
		t.Fatalf("workers after failed hook = %d", n)
	}
	if n := countRows(t, f.db, `SELECT COUNT(*) FROM worker_operations`); n != 0 {
		t.Fatalf("receipts after failed hook = %d", n)
	}
	calls := 0
	ok := workerrecords.Op{ID: "op-hook", Hook: func(ctx context.Context, tx *sql.Tx, receipt any) error {
		calls++
		return outboxHook("worker.created")(ctx, tx, receipt)
	}}
	_, err = f.store.CreateWorker(t.Context(), f.coord, ok, spec)
	testutil.FailErr(t, "create after failure", err)
	_, err = f.store.CreateWorker(t.Context(), f.coord, ok, spec)
	testutil.FailErr(t, "replay", err)
	if calls != 1 || countRows(t, f.db, `SELECT COUNT(*) FROM event_outbox WHERE topic = 'worker.created'`) != 1 {
		t.Fatalf("hook calls = %d; a replay must not emit again", calls)
	}
}

func TestCrossSessionDenial(t *testing.T) {
	f := newFixture(t)
	w, child := f.workerWithRound(t, "fixer", "child-1", "job-1")
	f.insertResult(t, "result-1", "job-1")

	_, err := f.store.Worker(t.Context(), f.other, w.WorkerID)
	wantErr(t, "read", err, workerrecords.ErrWorkerNotFound)
	_, err = f.store.Resolve(t.Context(), f.other, "fixer")
	wantErr(t, "resolve", err, workerrecords.ErrWorkerNotFound)
	_, err = f.store.AppendMessage(t.Context(), f.other, op("x-msg"), w.WorkerID, workerrecords.MessageSpec{Body: "hi"})
	wantErr(t, "message", err, workerrecords.ErrWorkerNotFound)
	_, err = f.store.AccountResult(t.Context(), f.other, op("x-account"), "result-1")
	wantErr(t, "account", err, workerrecords.ErrResultNotFound)
	_, err = f.store.History(t.Context(), f.other, w.WorkerID, 0, 10)
	wantErr(t, "history", err, workerrecords.ErrWorkerNotFound)

	// The worker's own session may message its coordinator but not act for it.
	_, err = f.store.AppendMessage(t.Context(), child, op("c-msg"), w.WorkerID, workerrecords.MessageSpec{Body: "note"})
	testutil.FailErr(t, "worker message", err)
	_, err = f.store.AccountResult(t.Context(), child, op("c-account"), "result-1")
	wantErr(t, "worker accounting", err, workerrecords.ErrCoordinatorOnly)
	_, err = f.store.SetChange(t.Context(), child, op("c-land"), "result-1", workerrecords.Change{State: workerrecords.ChangeRejected})
	wantErr(t, "worker discarding", err, workerrecords.ErrCoordinatorOnly)
	_, err = f.store.Close(t.Context(), child, op("c-close"), w.WorkerID, "done")
	wantErr(t, "worker closing", err, workerrecords.ErrCoordinatorOnly)

	// A worker cannot reach another worker of the same coordinator.
	other := f.createWorker(t, "create-peer", workerrecords.WorkerSpec{Name: "peer"})
	_, err = f.store.AppendMessage(t.Context(), child, op("c-peer"), other.WorkerID, workerrecords.MessageSpec{Body: "hello peer"})
	wantErr(t, "addressing a peer", err, workerrecords.ErrWorkerNotFound)
}

func TestRoundsAdmitOneUnsettledRound(t *testing.T) {
	f := newFixture(t)
	w, _ := f.workerWithRound(t, "fixer", "child-1", "job-1")
	f.insertJob(t, "job-2", coordSession, "child-1", "pending")
	_, err := f.store.AdmitRound(t.Context(), f.coord, op("admit-2"), w.WorkerID, workerrecords.RoundSpec{JobID: "job-2", ExecutionTarget: "local"})
	wantErr(t, "second unsettled round", err, workerrecords.ErrRoundUnsettled)
	_, err = f.store.Close(t.Context(), f.coord, op("close-early"), w.WorkerID, "withdrawn")
	wantErr(t, "close while working", err, workerrecords.ErrRoundUnsettled)

	settled, err := f.store.SettleRound(t.Context(), f.coord, op("settle-1"), w.WorkerID, "job-1", "cp-1")
	testutil.FailErr(t, "settle", err)
	rounds, err := f.store.Rounds(t.Context(), f.coord, w.WorkerID)
	testutil.FailErr(t, "rounds", err)
	f.insertResult(t, "result-1", "job-1")
	rounds, err = f.store.Rounds(t.Context(), f.coord, w.WorkerID)
	testutil.FailErr(t, "rounds with result", err)
	if rounds[0].CheckpointID != "cp-1" || settled.CheckpointID != "cp-1" || rounds[0].ResultID != "result-1" {
		t.Fatalf("settled round = %+v, receipt %+v", rounds[0], settled)
	}
	again, err := f.store.SettleRound(t.Context(), f.coord, op("settle-1b"), w.WorkerID, "job-1", "")
	testutil.FailErr(t, "settle again", err)
	if again != settled {
		t.Fatalf("second settlement = %+v, want %+v", again, settled)
	}
	r2, err := f.store.AdmitRound(t.Context(), f.coord, op("admit-2"), w.WorkerID, workerrecords.RoundSpec{JobID: "job-2", ExecutionTarget: "local"})
	testutil.FailErr(t, "admit after settlement", err)
	if r2.Ordinal != 2 {
		t.Fatalf("ordinal = %d want 2", r2.Ordinal)
	}

	_, err = f.store.SettleRound(t.Context(), f.coord, op("settle-2"), w.WorkerID, "job-2", "")
	testutil.FailErr(t, "settle 2", err)
	_, err = f.store.AdmitRound(t.Context(), f.coord, op("admit-2-again"), w.WorkerID, workerrecords.RoundSpec{JobID: "job-2", ExecutionTarget: "local"})
	wantErr(t, "a job in two rounds", err, workerrecords.ErrJobBinding)
	insertSession(t, f.db, "foreign-child", otherSession)
	f.insertJob(t, "job-foreign", otherSession, "foreign-child", "pending")
	_, err = f.store.AdmitRound(t.Context(), f.coord, op("admit-foreign"), w.WorkerID, workerrecords.RoundSpec{JobID: "job-foreign", ExecutionTarget: "local"})
	wantErr(t, "another session's job", err, workerrecords.ErrJobBinding)
	f.insertJob(t, "job-sibling", coordSession, "", "pending")
	_, err = f.store.AdmitRound(t.Context(), f.coord, op("admit-range"), w.WorkerID, workerrecords.RoundSpec{JobID: "job-sibling", ExecutionTarget: "local", InstructionFirstSeq: 1, InstructionLastSeq: 9})
	wantErr(t, "instructions not yet written", err, workerrecords.ErrInvalidRound)
	_, err = f.db.ExecContext(t.Context(), `UPDATE worker_rounds SET job_id = 'job-sibling' WHERE job_id = 'job-1'`)
	if err == nil {
		t.Fatal("settled round rewritten")
	}
}

// Concurrent admissions on one worker admit exactly one round, and concurrent
// messages get distinct consecutive sequences.
func TestConcurrentAllocation(t *testing.T) {
	f := newFixture(t)
	f.childActor(t, "child-1")
	w := f.createWorker(t, "create", workerrecords.WorkerSpec{Name: "busy", ChildSessionID: "child-1"})
	const n = 12
	for i := range n {
		f.insertJob(t, jobName(i), coordSession, "child-1", "pending")
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.store.AdmitRound(context.Background(), f.coord, op("admit-"+jobName(i)), w.WorkerID, workerrecords.RoundSpec{JobID: jobName(i), ExecutionTarget: "local"})
		}()
	}
	wg.Wait()
	admitted := 0
	for _, err := range errs {
		switch {
		case err == nil:
			admitted++
		case !errors.Is(err, workerrecords.ErrRoundUnsettled):
			t.Fatalf("admission error = %v", err)
		}
	}
	if admitted != 1 {
		t.Fatalf("admitted = %d, want exactly 1", admitted)
	}

	seqs := make([]int64, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := f.store.AppendMessage(context.Background(), f.coord, op("msg-"+jobName(i)), w.WorkerID, workerrecords.MessageSpec{Body: "instruction"})
			if err != nil {
				t.Errorf("append: %v", err)
			}
			seqs[i] = r.Seq
		}()
	}
	wg.Wait()
	slices.Sort(seqs)
	for i, seq := range seqs {
		if seq != int64(i+1) {
			t.Fatalf("sequences = %v, want 1..%d", seqs, n)
		}
	}
}

func jobName(i int) string { return "job-" + string(rune('a'+i)) }
