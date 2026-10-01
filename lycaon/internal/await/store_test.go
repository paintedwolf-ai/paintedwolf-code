package await

import (
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	return &Store{DB: database}
}

func armFixture(t *testing.T, store *Store, reason string) Lease {
	t.Helper()
	lease, err := store.Arm(t.Context(), Lease{
		SessionID: "session-1", ProjectID: testdbseed.DefaultProjectID,
		ToolCallID: "call-1", ProfileID: "implement", Reason: reason,
		Deadline:      time.Now().UTC().Add(10 * time.Minute),
		Conditions:    []Condition{{Kind: "http_ready", URL: "https://example.test/health", Method: "HEAD", StatusMin: 200, StatusMax: 399}},
		LoopbackPorts: []uint16{8080},
	})
	testutil.FailErr(t, "arm wait lease", err)
	return lease
}

func TestSettleLeaseCannotResolveReplacement(t *testing.T) {
	store := testStore(t)
	retired := armFixture(t, store, "first")
	active := armFixture(t, store, "second")

	won, err := store.SettleLease(t.Context(), retired.ID, "resolved", Condition{Kind: "http_ready"})
	testutil.FailErr(t, "settle retired wait lease", err)
	if won {
		t.Fatal("retired lease settled its replacement")
	}
	got, ok, err := store.ForSession(t.Context(), "session-1")
	testutil.FailErr(t, "read active wait lease", err)
	if !ok || got.ID != active.ID {
		t.Fatalf("active lease = %+v, want %s", got, active.ID)
	}

	won, err = store.SettleLease(t.Context(), active.ID, "resolved", Condition{Kind: "http_ready"})
	testutil.FailErr(t, "settle active wait lease", err)
	if !won {
		t.Fatal("active lease was not settled")
	}
}

func TestLatestResumeCandidateSkipsNewerNonresumableLease(t *testing.T) {
	store := testStore(t)
	resumable := armFixture(t, store, "resume me")
	won, err := store.SettleLease(t.Context(), resumable.ID, "interrupted", Condition{})
	testutil.FailErr(t, "interrupt wait lease", err)
	if !won {
		t.Fatal("interrupted lease was not settled")
	}
	nonresumable := armFixture(t, store, "do not resume")
	won, err = store.SettleLease(t.Context(), nonresumable.ID, "canceled", Condition{})
	testutil.FailErr(t, "cancel wait lease", err)
	if !won {
		t.Fatal("canceled lease was not settled")
	}

	got, ok, err := store.LatestResumeCandidate(t.Context(), "session-1")
	testutil.FailErr(t, "read resumable wait lease", err)
	if !ok || got.ID != resumable.ID || got.Status != "interrupted" {
		t.Fatalf("resumable lease = %+v, want %s", got, resumable.ID)
	}
}

func TestActiveRoundTripsConditionsAndAuthority(t *testing.T) {
	store := testStore(t)
	want := armFixture(t, store, "round trip")
	active, err := store.Active(t.Context())
	testutil.FailErr(t, "list active wait leases", err)
	if len(active) != 1 {
		t.Fatalf("active leases = %d, want 1", len(active))
	}
	got := active[0]
	if got.ID != want.ID || len(got.Conditions) != 1 || got.Conditions[0].URL != want.Conditions[0].URL ||
		len(got.LoopbackPorts) != 1 || got.LoopbackPorts[0] != 8080 {
		t.Fatalf("round-tripped lease = %+v, want %+v", got, want)
	}
}

func TestNewAgentWaitSupersedesOlderUndeliveredWake(t *testing.T) {
	store := testStore(t)
	older := armFixture(t, store, "older")
	won, err := store.SettleLease(t.Context(), older.ID, "resolved", Condition{Kind: "http_ready", Outcome: "satisfied"})
	testutil.FailErr(t, "settle older wait", err)
	if !won {
		t.Fatal("older wait settlement lost")
	}
	if pending, err := store.PendingAgentResumes(t.Context()); err != nil || len(pending) != 1 {
		t.Fatalf("pending before replacement = %+v err=%v", pending, err)
	}

	_ = armFixture(t, store, "replacement")
	if pending, err := store.PendingAgentResumes(t.Context()); err != nil || len(pending) != 0 {
		t.Fatalf("pending after replacement = %+v err=%v", pending, err)
	}
}

func TestInterruptSessionRetiresOnlyUndeliveredResults(t *testing.T) {
	for _, status := range []string{"interrupted", "canceled"} {
		t.Run(status, func(t *testing.T) {
			store := testStore(t)
			delivered := armFixture(t, store, "delivered")
			_, err := store.SettleLease(t.Context(), delivered.ID, "resolved", Condition{Kind: "timer"})
			testutil.FailErr(t, "settle delivered wait", err)
			testutil.FailErr(t, "acknowledge wait", store.MarkResumeDelivered(t.Context(), delivered.ID))
			pending := armFixture(t, store, "pending")
			_, err = store.SettleLease(t.Context(), pending.ID, "timed_out", Condition{Kind: "timer", Outcome: "timed_out"})
			testutil.FailErr(t, "settle pending wait", err)
			testutil.FailErr(t, "interrupt session", store.InterruptSession(t.Context(), "session-1", status))
			remaining, err := store.PendingAgentResumes(t.Context())
			testutil.FailErr(t, "read pending waits", err)
			if len(remaining) != 0 {
				t.Fatalf("interrupted results remain pending: %+v", remaining)
			}
			for id, want := range map[string]string{delivered.ID: "resolved", pending.ID: status} {
				var got string
				testutil.FailErr(t, "read wait status", store.DB.QueryRowContext(t.Context(), `SELECT status FROM wait_leases WHERE id = ?`, id).Scan(&got))
				if got != want {
					t.Errorf("wait %s status=%s want=%s", id, got, want)
				}
			}
		})
	}
}

func TestLatestResumeCandidateRetainsExpiredConditions(t *testing.T) {
	for _, status := range []string{"resolved", "interrupted", "timed_out"} {
		t.Run(status, func(t *testing.T) {
			store := testStore(t)
			_, found, err := store.LatestResumeCandidate(t.Context(), "session-1")
			testutil.FailErr(t, "read absent resume candidate", err)
			if found {
				t.Fatal("empty store returned a resume candidate")
			}
			want, err := store.Arm(t.Context(), Lease{
				SessionID: "session-1", ProjectID: testdbseed.DefaultProjectID,
				ToolCallID: "expired-call", ProfileID: "coordinator", Deadline: time.Now().UTC().Add(-time.Minute),
				Conditions: []Condition{{Kind: "port_ready", Host: "localhost", Port: 3001}},
			})
			testutil.FailErr(t, "arm expired wait", err)
			won, err := store.SettleLease(t.Context(), want.ID, status, Condition{})
			testutil.FailErr(t, "settle expired wait", err)
			if !won {
				t.Fatal("expired wait was not settled")
			}
			got, found, err := store.LatestResumeCandidate(t.Context(), "session-1")
			testutil.FailErr(t, "read expired resume candidate", err)
			if !found || got.ID != want.ID || !got.Deadline.Equal(want.Deadline) || len(got.Conditions) != 1 || got.Conditions[0].Port != 3001 {
				t.Fatalf("expired candidate lost conditions: %+v", got)
			}
		})
	}
}

func TestCompletionDeadlineRoundTripAndResume(t *testing.T) {
	store := testStore(t)
	want, err := store.Arm(t.Context(), Lease{SessionID: "session-1", ProjectID: testdbseed.DefaultProjectID, UntilComplete: true,
		Conditions: []Condition{{Kind: "process_done", Handles: []string{"command-1"}}}})
	testutil.FailErr(t, "arm completion-only lease", err)
	var absent bool
	testutil.FailErr(t, "read absent deadline", store.DB.QueryRowContext(t.Context(), `SELECT deadline_at IS NULL FROM wait_leases WHERE id = ?`, want.ID).Scan(&absent))
	if !absent {
		t.Fatal("completion-only lease has a deadline sentinel")
	}
	active, err := store.Active(t.Context())
	testutil.FailErr(t, "recover completion-only lease", err)
	if len(active) != 1 || !active[0].Deadline.IsZero() || !active[0].UntilComplete {
		t.Fatalf("active completion lease = %+v", active)
	}
	_, err = store.SettleLease(t.Context(), want.ID, "interrupted", Condition{})
	testutil.FailErr(t, "interrupt completion-only lease", err)
	got, found, err := store.LatestResumeCandidate(t.Context(), "session-1")
	testutil.FailErr(t, "read completion-only resume", err)
	if !found || !got.Deadline.IsZero() || !got.UntilComplete || got.ID != want.ID {
		t.Fatalf("completion resume = %+v found=%v", got, found)
	}
}

func TestBoundedCompletionLeaseKeepsModeAndDeadline(t *testing.T) {
	store := testStore(t)
	deadline := time.Now().UTC().Add(time.Minute).Truncate(time.Millisecond)
	want, err := store.Arm(t.Context(), Lease{SessionID: "session-1", ProjectID: testdbseed.DefaultProjectID,
		UntilComplete: true, Deadline: deadline,
		Conditions: []Condition{{Kind: "process_done", Handles: []string{"command-1"}}}})
	testutil.FailErr(t, "arm bounded completion lease", err)
	active, found, err := store.ForSession(t.Context(), "session-1")
	testutil.FailErr(t, "read bounded completion lease", err)
	if !found || !active.UntilComplete || !active.Deadline.Equal(deadline) {
		t.Fatalf("armed bounded completion lease = %+v", active)
	}
	_, err = store.SettleLease(t.Context(), want.ID, "interrupted", Condition{})
	testutil.FailErr(t, "interrupt bounded completion lease", err)
	got, found, err := store.LatestResumeCandidate(t.Context(), "session-1")
	testutil.FailErr(t, "read bounded completion resume", err)
	if !found || !got.UntilComplete || !got.Deadline.Equal(deadline) {
		t.Fatalf("bounded completion resume = %+v found=%v", got, found)
	}
}
