package call_test

import (
	"context"
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
			INSERT INTO sessions (id, project_id, owner_person_id, workspace_root_id, posture, status, created_at, activity_at, updated_at)
			VALUES (?, ?, (SELECT id FROM people WHERE role = 'owner'), ?, 'build', 'idle', ?, ?, ?)
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
