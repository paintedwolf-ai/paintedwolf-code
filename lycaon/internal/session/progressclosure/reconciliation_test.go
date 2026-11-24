package progressclosure_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/progressclosure"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/reporting"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"time"
)

type ackFixture struct {
	sqlDB   *db.Store
	records *workerrecords.Store
	store   *progress.SQLStore
	closure *progressclosure.Service
	coord   workerrecords.Actor
}

func newAckFixture(t *testing.T) *ackFixture {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "progressclosure.db")
	testdbseed.InsertSession(t, sqlDB, "session", testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, sqlDB, "foreign", testdbseed.DefaultProjectID)
	testdbseed.InsertWorkflowRun(t, sqlDB, "run-session", "session", testdbseed.DefaultProjectID)
	testdbseed.InsertWorkflowRun(t, sqlDB, "run-foreign", "foreign", testdbseed.DefaultProjectID)

	records := workerrecords.New(sqlDB)
	repos := sessionstore.NewSQL(sqlDB)
	store := progress.NewSQLStore(sqlDB, repos)
	closure := progressclosure.New()
	closure.SetProgress(store)
	closure.SetAccounts(records)
	closure.SetNames(records)

	return &ackFixture{
		sqlDB:   sqlDB,
		records: records,
		store:   store,
		closure: closure,
		coord:   workerrecords.Actor{SessionID: "session", Role: workerrecords.RoleCoordinator, PrincipalID: "session"},
	}
}

func (f *ackFixture) seedWorkerResult(t *testing.T, rootSession, name, childSession, jobID string) (string, string) {
	t.Helper()
	now := timelayout.Format(time.Now().UTC())
	_, err := f.sqlDB.ExecContext(t.Context(), `INSERT INTO sessions (
			id, project_id, owner_person_id, posture, status, parent_session_id, created_at, activity_at, updated_at, root_session_id
		) VALUES (?, ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', ?, ?, ?, ?, ?)`,
		childSession, testdbseed.DefaultProjectID, rootSession, now, now, now, rootSession)
	testutil.FailErr(t, "insert child session "+childSession, err)

	actor := workerrecords.Actor{SessionID: rootSession, Role: workerrecords.RoleCoordinator, PrincipalID: rootSession}
	w, err := f.records.CreateWorker(t.Context(), actor, workerrecords.Op{ID: "create-" + jobID}, workerrecords.WorkerSpec{
		Name: name, AgentType: "implementer", ScopeMode: workerrecords.ScopeWrite, ChildSessionID: childSession,
	})
	testutil.FailErr(t, "create worker "+name, err)

	_, err = f.sqlDB.ExecContext(t.Context(), `INSERT INTO worker_jobs (
			id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, created_at
		) VALUES (?, ?, ?, ?, 'implementer', 'running', 'prompt', 'brief', ?)`,
		jobID, testdbseed.DefaultProjectID, rootSession, childSession, now)
	testutil.FailErr(t, "insert job "+jobID, err)

	round, err := f.records.AdmitRound(t.Context(), actor, workerrecords.Op{ID: "admit-" + jobID}, w.WorkerID, workerrecords.RoundSpec{
		JobID: jobID, ExecutionTarget: "local",
	})
	testutil.FailErr(t, "admit round "+jobID, err)

	handle := fmt.Sprintf("%s#%d", name, round.Ordinal)
	_, err = f.sqlDB.ExecContext(t.Context(), `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES (?, ?, 'complete', ?)`,
		handle, jobID, now)
	testutil.FailErr(t, "insert result "+handle, err)

	_, err = db.New(f.sqlDB).SettleWorkerRoundByJob(t.Context(), db.SettleWorkerRoundByJobParams{
		SettledAt: db.NullString(now), CheckpointID: "", JobID: jobID,
	})
	testutil.FailErr(t, "settle round "+jobID, err)
	return w.WorkerID, handle
}

type updateProgressReply struct {
	Status              string                   `json:"status"`
	DocumentChanged     bool                     `json:"document_changed"`
	StateChanged        bool                     `json:"state_changed"`
	ClosedItemIDs       []string                 `json:"closed_item_ids"`
	AcknowledgedResults []string                 `json:"acknowledged_results"`
	Snapshot            progress.RequestSnapshot `json:"snapshot"`
}

func unaccountedHandles(closure *progressclosure.Service, session string) []string {
	items := closure.UnaccountedResults(session)
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.Handle
	}
	return out
}

func (f *ackFixture) seedRows(t *testing.T, specs ...progress.RowSpec) progress.Snapshot {
	t.Helper()
	out, err := f.store.Apply(t.Context(), "session", progress.Mutation{Replace: specs}, progress.ApplyOptions{})
	testutil.FailErr(t, "apply seed", err)
	return out.Snapshot
}

func TestWorkerAcknowledgementDoesNotRequireClosingUnfinishedRows(t *testing.T) {
	f := newAckFixture(t)
	f.seedRows(t,
		progress.RowSpec{Label: "Survey", State: "done"},
		progress.RowSpec{Label: "Implement"},
		progress.RowSpec{Label: "Verify"},
	)
	_, handle := f.seedWorkerResult(t, "session", "scout", "child-1", "job-1")
	f.closure.Arm(t.Context(), "session", "job-1")

	handler := reporting.ProgressHandler(f.store, func(_ context.Context, s string) string { return s }, f.closure)
	out := &tools.ToolInvocationOut{}
	body, err := handler(t.Context(), map[string]any{
		"acknowledge_results": []any{handle},
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "session"},
		Effects:  tools.InvocationEffects{Out: out},
	})
	testutil.FailErr(t, "acknowledge result", err)
	if out.Facts.WorkflowAdvanced == nil || !*out.Facts.WorkflowAdvanced {
		t.Fatal("result acknowledgement did not publish workflow advancement")
	}

	var result updateProgressReply
	testutil.FailErr(t, "decode result", json.Unmarshal([]byte(body), &result))
	if result.Status != "unchanged" || !slices.Equal(result.AcknowledgedResults, []string{handle}) || len(result.Snapshot.Rows) != 3 {
		t.Fatalf("acknowledgement result = %+v", result)
	}
	if len(unaccountedHandles(f.closure, "session")) != 0 {
		t.Fatalf("unaccounted handles remained: %v", unaccountedHandles(f.closure, "session"))
	}
}

func TestAcknowledgeResultsResolvesOpenClosedAndRejectsForeign(t *testing.T) {
	f := newAckFixture(t)
	f.seedRows(t, progress.RowSpec{Label: "Deliver"})
	workerID, handle := f.seedWorkerResult(t, "session", "fixer", "child-1", "job-1")
	_, foreignHandle := f.seedWorkerResult(t, "foreign", "foreigner", "child-foreign", "job-foreign")
	f.closure.Arm(t.Context(), "session", "job-1")

	handler := reporting.ProgressHandler(f.store, func(_ context.Context, s string) string { return s }, f.closure)
	_, err := handler(t.Context(), map[string]any{
		"acknowledge_results": []any{foreignHandle},
	}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "PROGRESS_RESULT_HANDLE_UNKNOWN" {
		t.Fatalf("foreign handle err = %v, want PROGRESS_RESULT_HANDLE_UNKNOWN", err)
	}

	_, err = handler(t.Context(), map[string]any{
		"acknowledge_results": []any{handle},
	}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}})
	testutil.FailErr(t, "acknowledge open worker", err)

	_, err = f.records.Close(t.Context(), f.coord, workerrecords.Op{ID: "close-fixer"}, workerID, "settled")
	testutil.FailErr(t, "close worker", err)

	// Idempotent retry after the worker closes still resolves and succeeds.
	body, err := handler(t.Context(), map[string]any{
		"acknowledge_results": []any{handle},
	}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}})
	testutil.FailErr(t, "idempotent acknowledge after close", err)
	var result updateProgressReply
	testutil.FailErr(t, "decode idempotent result", json.Unmarshal([]byte(body), &result))
	if !slices.Equal(result.AcknowledgedResults, []string{handle}) {
		t.Fatalf("acknowledged_results after close = %v, want [%s]", result.AcknowledgedResults, handle)
	}
}

func TestPartialAcknowledgementKeepsUnacknowledgedSiblingLatched(t *testing.T) {
	f := newAckFixture(t)
	before := f.seedRows(t, progress.RowSpec{Label: "Deliver"}, progress.RowSpec{Label: "Verify"})
	_, h1 := f.seedWorkerResult(t, "session", "fixer", "child-1", "job-1")
	_, h2 := f.seedWorkerResult(t, "session", "verifier", "child-2", "job-2")
	f.closure.Arm(t.Context(), "session", "job-1")
	f.closure.Arm(t.Context(), "session", "job-2")

	handler := reporting.ProgressHandler(f.store, func(_ context.Context, s string) string { return s }, f.closure)
	body, err := handler(t.Context(), map[string]any{
		"update":              []any{map[string]any{"id": before.Rows[0].ID, "state": "done"}},
		"acknowledge_results": []any{h1},
	}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}})
	testutil.FailErr(t, "close one deliverable and ack one handle", err)

	var result updateProgressReply
	testutil.FailErr(t, "decode result", json.Unmarshal([]byte(body), &result))
	if result.Status != "updated" || !slices.Equal(result.ClosedItemIDs, []string{"p1"}) || !slices.Equal(result.AcknowledgedResults, []string{h1}) {
		t.Fatalf("result = %+v", result)
	}
	if got := unaccountedHandles(f.closure, "session"); !slices.Equal(got, []string{h2}) {
		t.Fatalf("remaining unaccounted handles = %v, want [%s]", got, h2)
	}
}

type failingAccounts struct {
	inner progressclosure.Accounts
	err   error
}

func (a *failingAccounts) ResolveResultForAckTx(ctx context.Context, tx *sql.Tx, actor workerrecords.Actor, resultID string) (workerrecords.ResultRef, error) {
	return a.inner.ResolveResultForAckTx(ctx, tx, actor, resultID)
}

func (a *failingAccounts) AccountJobsTx(ctx context.Context, tx *sql.Tx, jobIDs []string) error {
	if a.err != nil {
		return a.err
	}
	return a.inner.AccountJobsTx(ctx, tx, jobIDs)
}

func TestSingleTxRollbackAcrossRowsTranscriptAndWorkerDisposition(t *testing.T) {
	f := newAckFixture(t)
	before := f.seedRows(t, progress.RowSpec{Label: "Deliver"})
	workerID, handle := f.seedWorkerResult(t, "session", "fixer", "child-1", "job-1")
	f.closure.Arm(t.Context(), "session", "job-1")

	fault := &failingAccounts{inner: f.records, err: errors.New("injected accounting failure")}
	f.closure.SetAccounts(fault)

	var beforeTranscript int
	testutil.FailErr(t, "count transcript before", f.sqlDB.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM messages WHERE session_id = 'session'`).Scan(&beforeTranscript))

	handler := reporting.ProgressHandler(f.store, func(_ context.Context, s string) string { return s }, f.closure)
	_, err := handler(t.Context(), map[string]any{
		"add":                 []any{map[string]any{"label": "Follow-up"}},
		"acknowledge_results": []any{handle},
	}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}})
	if err == nil {
		t.Fatal("expected injected accounting error")
	}

	after, err := f.store.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "snapshot after failed commit", err)
	if len(after.Rows) != 1 || after.DocumentVersion() != before.DocumentVersion() {
		t.Fatalf("rows half-committed on accounting failure: %+v", after)
	}
	var afterTranscript int
	testutil.FailErr(t, "count transcript after", f.sqlDB.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM messages WHERE session_id = 'session'`).Scan(&afterTranscript))
	if afterTranscript != beforeTranscript {
		t.Fatalf("transcript rows = %d, want %d", afterTranscript, beforeTranscript)
	}
	disps, err := f.records.Dispositions(t.Context(), f.coord, workerID)
	testutil.FailErr(t, "dispositions after rollback", err)
	if len(disps) != 0 {
		t.Fatalf("dispositions after rollback = %+v, want empty", disps)
	}
	if got := unaccountedHandles(f.closure, "session"); !slices.Equal(got, []string{handle}) {
		t.Fatalf("latch cleared despite rollback: %v", got)
	}

	// Clearing the fault lets the exact retry commit cleanly without duplicating the added row.
	fault.err = nil
	_, err = handler(t.Context(), map[string]any{
		"add":                 []any{map[string]any{"label": "Follow-up"}},
		"acknowledge_results": []any{handle},
	}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}})
	testutil.FailErr(t, "retry after rollback", err)

	committed, err := f.store.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "snapshot after retry", err)
	if len(committed.Rows) != 2 || committed.Rows[1].ID != "p2" {
		t.Fatalf("rows after retry = %+v, want [p1 p2]", committed.Rows)
	}
	if got := unaccountedHandles(f.closure, "session"); len(got) != 0 {
		t.Fatalf("unaccounted handles after retry = %v, want empty", got)
	}
}

func TestReleaseDropsAccountedFinishes(t *testing.T) {
	f := newAckFixture(t)
	f.seedRows(t, progress.RowSpec{Label: "Deliver"})
	_, h1 := f.seedWorkerResult(t, "session", "fixer", "child-1", "job-1")
	_, h2 := f.seedWorkerResult(t, "session", "docs", "child-2", "job-2")
	f.closure.Arm(t.Context(), "session", "job-1")
	f.closure.Arm(t.Context(), "session", "job-2")
	if got := unaccountedHandles(f.closure, "session"); !slices.Equal(got, []string{h2, h1}) && !slices.Equal(got, []string{h1, h2}) {
		t.Fatalf("initial unaccounted = %v", got)
	}
	f.closure.Release("session", []string{"job-1"})
	if got := unaccountedHandles(f.closure, "session"); !slices.Equal(got, []string{h2}) {
		t.Fatalf("after release = %v, want [%s]", got, h2)
	}
	f.closure.Release("session", []string{"job-2"})
	if got := unaccountedHandles(f.closure, "session"); len(got) != 0 {
		t.Fatalf("latch kept after last release: %v", got)
	}
}
