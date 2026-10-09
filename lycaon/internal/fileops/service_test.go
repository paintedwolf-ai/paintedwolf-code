package fileops

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func requestFixture() Request {
	return Request{ID: "operation", ProjectID: "project", PersonID: "person", Operation: "renameProjectSource", Method: "POST", URI: "/source/rename", InputDigest: "digest", RootScope: "root"}
}

func TestFileOperationDurableReceiptAndRestart(t *testing.T) {
	database := testdbfixture.Open(t, "operations.db")
	testdbseed.InsertProject(t, database, "project")
	request := requestFixture()
	testutil.FailErr(t, "read host owner", database.QueryRowContext(t.Context(), `SELECT id FROM people WHERE role='owner'`).Scan(&request.PersonID))
	request.SessionID, request.Turn = "affiliated session", 17
	service := NewService(NewStore(database))
	run, _, err := service.Admit(t.Context(), request, false)
	testutil.FailErr(t, "persist admission", err)
	run.Report(t.Context(), "copying", 1234, 8<<30)
	restarted := NewService(NewStore(database))
	testutil.FailErr(t, "reconcile after restart", restarted.Recover(t.Context(), func(_ context.Context, held Request) (Outcome, bool, error) {
		if held.EntriesProcessed != 1234 || held.BytesProcessed != 8<<30 || held.SessionID != request.SessionID || held.Turn != 17 {
			t.Fatalf("lost durable request fields: %+v", held)
		}
		return Outcome{Status: 204}, true, nil
	}))
	completed, created, err := restarted.Admit(t.Context(), request, true)
	testutil.FailErr(t, "read completed request", err)
	if created || completed.Snapshot().State != "completed" || completed.Snapshot().ResponseStatus != 204 {
		t.Fatalf("replayed committed effect: %+v", completed.Snapshot())
	}
	listed, err := restarted.List(t.Context(), "project", request.PersonID)
	testutil.FailErr(t, "list owner requests", err)
	if len(listed) != 1 {
		t.Fatalf("request count=%d", len(listed))
	}
	other, err := restarted.List(t.Context(), "project", "another person")
	testutil.FailErr(t, "list other person's requests", err)
	if len(other) != 0 {
		t.Fatal("request crossed owner scope")
	}
}

func TestFileOperationObserversSeeEveryPersistedState(t *testing.T) {
	service := NewService(NewStore(nil))
	var states []string
	service.Observe(func(r Request) { states = append(states, r.State+"/"+r.Phase) })
	run, _, err := service.Admit(t.Context(), requestFixture(), false)
	testutil.FailErr(t, "admit", err)
	run.Execute(t.Context(), func(ctx context.Context) Outcome {
		testutil.FailErr(t, "commit boundary", run.BeginEffect(ctx))
		return Outcome{Status: 204}
	})
	want := []string{"queued/preparing", "running/preparing", "running/applying", "completed/applying"}
	if !slices.Equal(states, want) {
		t.Fatalf("observed states = %v, want %v", states, want)
	}
	if _, _, err := service.Admit(t.Context(), requestFixture(), false); err != nil {
		t.Fatalf("join completed: %v", err)
	}
	// Joining or replaying a retained result persists nothing new.
	if len(states) != len(want) {
		t.Fatalf("join notified observers: %v", states)
	}
}

func TestFileOperationJoinsAndRetainsCompletedResult(t *testing.T) {
	service := NewService(NewStore(nil))
	run, created, err := service.Admit(t.Context(), requestFixture(), false)
	testutil.FailErr(t, "admit", err)
	if !created {
		t.Fatal("first request was not created")
	}
	joined, created, err := service.Admit(t.Context(), requestFixture(), false)
	testutil.FailErr(t, "join", err)
	if joined != run || created {
		t.Fatal("duplicate admission created work")
	}
	var calls atomic.Int32
	run.Execute(t.Context(), func(context.Context) Outcome { calls.Add(1); return Outcome{Status: 200, Body: `{"path":"moved"}`} })
	replay, created, err := service.Admit(t.Context(), requestFixture(), true)
	testutil.FailErr(t, "completed retry", err)
	if created || calls.Load() != 1 || replay.Snapshot().ResponseBody != `{"path":"moved"}` {
		t.Fatal("completed result was not retained")
	}
	changed := requestFixture()
	changed.RootScope = "replacement root"
	if _, _, err := service.Admit(t.Context(), changed, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed root error=%v", err)
	}
}

func TestFileOperationCancellationCommitBoundary(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "preparing", true: "applying"}[commit], func(t *testing.T) {
			service := NewService(NewStore(nil))
			run, _, err := service.Admit(t.Context(), requestFixture(), false)
			testutil.FailErr(t, "admit", err)
			run.Execute(t.Context(), func(ctx context.Context) Outcome {
				if commit {
					testutil.FailErr(t, "commit boundary", run.BeginEffect(ctx))
				}
				err := service.Cancel("operation")
				if commit {
					if !errors.Is(err, ErrNotCancelable) {
						t.Fatalf("cancel after commit=%v", err)
					}
					return Outcome{Status: 204}
				}
				testutil.FailErr(t, "cancel preparation", err)
				if err := run.BeginEffect(ctx); !errors.Is(err, context.Canceled) {
					t.Fatalf("commit after cancel=%v", err)
				}
				<-ctx.Done()
				return Outcome{Status: 500}
			})
			want := "canceled"
			if commit {
				want = "completed"
			}
			if got := run.Snapshot().State; got != want {
				t.Fatalf("state=%s want=%s", got, want)
			}
		})
	}
}

func TestFileOperationRecoveryReconcilesOnlyCommittedEffects(t *testing.T) {
	store := NewStore(nil)
	service := NewService(store)
	for _, id := range []string{"applied", "prepared"} {
		request := requestFixture()
		request.ID = id
		_, _, err := service.Admit(t.Context(), request, false)
		testutil.FailErr(t, "admit", err)
	}
	restarted := NewService(store)
	testutil.FailErr(t, "recover", restarted.Recover(t.Context(), func(_ context.Context, r Request) (Outcome, bool, error) {
		return Outcome{Status: 204}, r.ID == "applied", nil
	}))
	for id, want := range map[string]string{"applied": "completed", "prepared": "interrupted"} {
		got, err := restarted.Get(t.Context(), id)
		testutil.FailErr(t, "read recovered result", err)
		if got.State != want {
			t.Fatalf("%s state=%s want=%s", id, got.State, want)
		}
	}
	request := requestFixture()
	request.ID = "prepared"
	run, created, err := restarted.Admit(t.Context(), request, true)
	testutil.FailErr(t, "explicit retry", err)
	if !created {
		t.Fatal("retry did not create work")
	}
	run.Execute(t.Context(), func(context.Context) Outcome { return Outcome{Status: 204} })
}

func TestFileOperationRecoveryPagesUnsettledReceipts(t *testing.T) {
	database := testdbfixture.Open(t, "operations.db")
	testdbseed.InsertProject(t, database, "project")
	store := NewStore(database)
	request := requestFixture()
	testutil.FailErr(t, "read owner", database.QueryRowContext(t.Context(), `SELECT id FROM people WHERE role='owner'`).Scan(&request.PersonID))
	request.CreatedAt, request.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	request.CompletedAt = new(request.UpdatedAt)
	for i := 0; i < 205; i++ {
		request.ID, request.State = fmt.Sprintf("operation-%03d", i), "failed"
		testutil.FailErr(t, "persist unsettled receipt", store.Insert(t.Context(), request))
	}
	seen := make(map[string]bool)
	testutil.FailErr(t, "page recovery", store.Recover(t.Context(), func(_ context.Context, r Request) (Outcome, bool, error) {
		if seen[r.ID] {
			t.Fatalf("receipt revisited: %s", r.ID)
		}
		seen[r.ID] = true
		return Outcome{Status: 204}, len(seen)%2 == 0, nil
	}))
	if len(seen) != 205 {
		t.Fatalf("recovered %d receipts", len(seen))
	}
}

// Active work sorts by submission and finished work by completion, newest first
// in each group, so per-second progress saves do not reshuffle the list.
func TestFileOperationListOrdersBySubmissionThenCompletion(t *testing.T) {
	database := testdbfixture.Open(t, "operations.db")
	testdbseed.InsertProject(t, database, "project")
	store := NewStore(database)
	owner := ""
	testutil.FailErr(t, "read owner", database.QueryRowContext(t.Context(), `SELECT id FROM people WHERE role='owner'`).Scan(&owner))
	at := func(sec int) time.Time { return time.Date(2026, 9, 18, 12, 0, sec, 0, time.UTC) }
	insert := func(id, state string, created, updated int, completed *time.Time) {
		t.Helper()
		request := requestFixture()
		request.ID, request.PersonID, request.State = id, owner, state
		request.CreatedAt, request.UpdatedAt, request.CompletedAt = at(created), at(updated), completed
		testutil.FailErr(t, "insert "+id, store.Insert(t.Context(), request))
	}
	insert("running-old-but-progressing", "running", 1, 59, nil)
	insert("queued-new", "queued", 5, 5, nil)
	insert("finished-late", "completed", 0, 40, new(at(40)))
	insert("finished-early", "failed", 2, 50, new(at(20)))
	requests, err := store.List(t.Context(), "project", owner)
	testutil.FailErr(t, "list", err)
	got := make([]string, 0, len(requests))
	for _, request := range requests {
		got = append(got, request.ID)
	}
	want := []string{"queued-new", "running-old-but-progressing", "finished-late", "finished-early"}
	if !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// A request the host died under stopped at its last recorded progress, not at
// the next boot's recovery pass.
func TestFileOperationRecoveryDatesInterruptionAtLastProgress(t *testing.T) {
	database := testdbfixture.Open(t, "operations.db")
	testdbseed.InsertProject(t, database, "project")
	store := NewStore(database)
	request := requestFixture()
	testutil.FailErr(t, "read owner", database.QueryRowContext(t.Context(), `SELECT id FROM people WHERE role='owner'`).Scan(&request.PersonID))
	lastProgress := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	request.State, request.CreatedAt, request.UpdatedAt = "running", lastProgress.Add(-time.Minute), lastProgress
	testutil.FailErr(t, "insert", store.Insert(t.Context(), request))
	testutil.FailErr(t, "recover", store.Recover(t.Context(), func(context.Context, Request) (Outcome, bool, error) {
		return Outcome{}, false, nil
	}))
	got, err := store.Get(t.Context(), request.ID)
	testutil.FailErr(t, "get", err)
	if got.State != "interrupted" || got.CompletedAt == nil || !got.CompletedAt.Equal(lastProgress) {
		t.Fatalf("recovered = %s at %v, want interrupted at %s", got.State, got.CompletedAt, lastProgress)
	}
}
