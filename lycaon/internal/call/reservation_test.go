package call_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/call"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"

	"github.com/lycaon/lycaon/internal/testdbseed"
)

func openCallTestDB(t *testing.T) *db.Store {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "call.db")
	dir := "/tmp/a"
	rootID := testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, sid := range []string{"sess-a", "sess-b"} {
		_, err := sqlDB.ExecContext(t.Context(), `
			INSERT INTO sessions (id, project_id, owner_person_id, workspace_root_id, posture, status, created_at, activity_at, updated_at, root_session_id, mode)
			VALUES (?, ?, (SELECT id FROM people WHERE role = 'owner'), ?, 'build', 'idle', ?, ?, ?, ?1, 'agent')
		`, sid, testdbseed.DefaultProjectID, rootID, now, now, now)
		testutil.FailErr(t, "insert session", err)
	}
	return sqlDB
}

func TestReserveConflictDifferentAgent(t *testing.T) {
	ctx := context.Background()
	sqlDB := openCallTestDB(t)
	lookup := call.StoreSessionLookup{Get: func(_ context.Context, id string) (string, error) {
		if id == "sess-a" || id == "sess-b" {
			return "/tmp/a", nil
		}
		return "", call.ErrSessionNotFound
	}}
	mgr := call.NewSQLManager(sqlDB, lookup)

	out, err := mgr.Reserve(ctx, "sess-a", []string{"src/main.go"}, "job-a")
	testutil.FailErr(t, "reserve a", err)
	if len(out.Reserved) != 1 {
		t.Fatalf("reserved = %#v", out.Reserved)
	}
	out, err = mgr.Reserve(ctx, "sess-b", []string{"src/main.go"}, "job-b")
	testutil.FailErr(t, "reserve b", err)
	if len(out.Blocked) != 1 {
		t.Fatalf("blocked = %#v", out.Blocked)
	}
}

func TestReserveIdempotentSameAgent(t *testing.T) {
	ctx := context.Background()
	sqlDB := openCallTestDB(t)
	lookup := call.StoreSessionLookup{Get: func(_ context.Context, id string) (string, error) {
		return "/tmp/a", nil
	}}
	mgr := call.NewSQLManager(sqlDB, lookup)
	_, err := mgr.Reserve(ctx, "sess-a", []string{"src/a.go"}, "job-a")
	testutil.FailErr(t, "reserve first", err)
	out, err := mgr.Reserve(ctx, "sess-a", []string{"src/a.go"}, "job-a")
	testutil.FailErr(t, "reserve second", err)
	if len(out.Reserved) != 1 || len(out.Blocked) != 0 {
		t.Fatalf("out = %#v", out)
	}
}

func TestReserveRejectsPathEscape(t *testing.T) {
	ctx := context.Background()
	sqlDB := openCallTestDB(t)
	mgr := call.NewSQLManager(sqlDB, call.StoreSessionLookup{Get: func(context.Context, string) (string, error) {
		return "/tmp/a", nil
	}})
	_, err := mgr.Reserve(ctx, "sess-a", []string{"../escape.go"}, "job-a")
	if err == nil {
		t.Fatal("expected path escape error")
	}
}

func TestListActiveReservations(t *testing.T) {
	ctx := context.Background()
	sqlDB := openCallTestDB(t)
	lookup := call.StoreSessionLookup{Get: func(_ context.Context, id string) (string, error) {
		if id == "sess-a" {
			return "/tmp/a", nil
		}
		return "", call.ErrSessionNotFound
	}}
	mgr := call.NewSQLManager(sqlDB, lookup)
	_, err := mgr.Reserve(ctx, "sess-a", []string{"pkg/registry.py"}, "job-a")
	testutil.FailErr(t, "reserve", err)
	got, err := mgr.ListActiveReservations(ctx, "sess-a")
	testutil.FailErr(t, "list", err)
	if len(got) != 1 || got[0].Path != "pkg/registry.py" || got[0].Agent != "job-a" {
		t.Fatalf("list = %+v", got)
	}
}

// A worker's earlier round holds its paths for the worker: its next round
// may write them, while another worker is still blocked.
func TestReserveLetsAWorkerKeepItsPathsAcrossRounds(t *testing.T) {
	ctx := context.Background()
	sqlDB := openCallTestDB(t)
	records := workerrecords.New(sqlDB)
	coord := workerrecords.Actor{SessionID: "sess-a", Role: workerrecords.RoleCoordinator, PrincipalID: "sess-a"}
	w, err := records.CreateWorker(ctx, coord, workerrecords.Op{ID: "create"}, workerrecords.WorkerSpec{Name: "fixer", AgentType: "implementer", ScopeMode: "write"})
	testutil.FailErr(t, "create worker", err)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, job := range []string{"job-1", "job-2", "job-other"} {
		_, err := sqlDB.ExecContext(ctx, `INSERT INTO worker_jobs (id, project_id, parent_session_id, agent_type, status, prompt, brief, created_at)
			VALUES (?, ?, 'sess-a', 'implementer', 'running', 'p', 'b', ?)`, job, testdbseed.DefaultProjectID, now)
		testutil.FailErr(t, "insert job", err)
	}
	for _, job := range []string{"job-1", "job-2"} {
		_, err := records.AdmitRound(ctx, coord, workerrecords.Op{ID: "admit-" + job}, w.WorkerID, workerrecords.RoundSpec{JobID: job, ExecutionTarget: "local"})
		testutil.FailErr(t, "admit "+job, err)
		if job == "job-1" {
			_, err = db.New(sqlDB).SettleWorkerRoundByJob(ctx, db.SettleWorkerRoundByJobParams{SettledAt: db.NullString(now), JobID: job})
			testutil.FailErr(t, "settle", err)
		}
	}
	lookup := call.StoreSessionLookup{Get: func(context.Context, string) (string, error) { return "/tmp/a", nil }}
	mgr := call.NewSQLManager(sqlDB, lookup)
	_, err = mgr.Reserve(ctx, "sess-a", []string{"src/main.go"}, "job-1")
	testutil.FailErr(t, "reserve in round 1", err)
	out, err := mgr.Reserve(ctx, "sess-a", []string{"src/main.go"}, "job-2")
	testutil.FailErr(t, "reserve in round 2", err)
	if len(out.Blocked) != 0 {
		t.Fatalf("round 2 blocked by its own worker: %#v", out.Blocked)
	}
	out, err = mgr.Reserve(ctx, "sess-a", []string{"src/main.go"}, "job-other")
	testutil.FailErr(t, "reserve another worker", err)
	if len(out.Blocked) != 1 {
		t.Fatalf("another worker was not blocked: %#v", out)
	}
}
