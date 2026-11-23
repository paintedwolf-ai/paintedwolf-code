package watch

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCapacityLimitsSlotReleaseAndDerivedExemption(t *testing.T) {
	store, _, _, owner := openTestWatchStore(t)

	var handles []string
	for i := range MaxLiveExplicitWatches {
		rcpt, err := store.Register(t.Context(), RegisterRequest{
			Owner:      owner,
			Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{fmt.Sprintf("cmd-%d", i)}}},
		})
		testutil.FailErr(t, "register up to live cap", err)
		handles = append(handles, rcpt.Handle)
	}

	_, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-over"}}},
	})
	var capErr *CapacityError
	if !errors.As(err, &capErr) {
		t.Fatalf("expected CapacityError at 9th live watch, got %v", err)
	}
	if capErr.LiveCount != MaxLiveExplicitWatches || len(capErr.ReclaimableHandles) != MaxLiveExplicitWatches {
		t.Fatalf("unexpected capacity details: %+v", capErr)
	}

	// Derived Bash background observations do not consume explicit watch capacity.
	derived, err := store.Register(t.Context(), RegisterRequest{
		Owner:                owner,
		DerivedCommandHandle: "bg-job-1",
		Conditions:           []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"bg-job-1"}}},
	})
	testutil.FailErr(t, "register derived watch at full explicit quota", err)
	if derived.Handle == "" {
		t.Fatal("derived watch missing handle")
	}

	// Canceling one explicit handle immediately frees capacity without another cleanup step.
	_, err = store.Cancel(t.Context(), owner, handles[0])
	testutil.FailErr(t, "cancel watch-1 to free capacity", err)

	replacement, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-reused-slot"}}},
	})
	testutil.FailErr(t, "register after cancel freed slot", err)
	if replacement.Handle == "" {
		t.Fatal("expected replacement registration to succeed")
	}
}

func TestInlineByteCapBatchCapAndCompactionPreservesFailureThenRecovery(t *testing.T) {
	store, clock, _, owner := openTestWatchStore(t)

	rcpt, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: "http_ready", URL: "http://127.0.0.1:8080/health"}},
	})
	testutil.FailErr(t, "register repeat watch", err)

	// Oversized inline payload (> 2 KiB) is clamped and retains a reference.
	largeReport := strings.Repeat("x", 4096)
	m0, ok, err := store.RecordMatch(t.Context(), rcpt.WatchID, MatchInput{
		SourceKind:   "http_ready",
		OccurrenceID: "occ-0-fail",
		Outcome:      "failed",
		Evidence:     map[string]any{"report": largeReport},
	}, nil)
	testutil.FailErr(t, "record oversized failure match", err)
	if !ok || len(m0.Evidence) > MaxInlineMatchBytes || m0.RetainedRef == "" {
		t.Fatalf("oversized match not bounded: len=%d ref=%q", len(m0.Evidence), m0.RetainedRef)
	}

	// Push 35 more recovery matches so buffered matches exceed MaxBufferedMatches (32).
	for i := 1; i <= 35; i++ {
		_, _, err := store.RecordMatch(t.Context(), rcpt.WatchID, MatchInput{
			SourceKind:   "http_ready",
			OccurrenceID: fmt.Sprintf("occ-%d", i),
			Outcome:      "satisfied",
			Evidence:     map[string]any{"seq": i, "pad": strings.Repeat("y", 600)},
		}, nil)
		testutil.FailErr(t, "record burst match", err)
	}

	view, err := store.Inspect(t.Context(), owner, rcpt.Handle)
	testutil.FailErr(t, "inspect compacted watch", err)
	if view.PendingMatches != MaxBufferedMatches {
		t.Fatalf("pending matches = %d, want %d", view.PendingMatches, MaxBufferedMatches)
	}
	if view.Overflow == nil || !view.Overflow.HadFailure || !view.Overflow.HadRecovery || view.Overflow.CompactedCount != 4 {
		t.Fatalf("compaction lost failure->recovery visibility: %+v", view.Overflow)
	}

	// Claiming batch bounds payload to MaxBatchBytes (4 KiB), leaving remaining matches pending.
	batch, err := store.SelectOrPark(t.Context(), SelectRequest{
		Owner:     owner,
		Handles:   []string{rcpt.Handle},
		AllowPark: false,
		ParkLease: awaitstore.Lease{ToolCallID: "claim-1", Deadline: clock.Now().Add(time.Minute)},
	})
	testutil.FailErr(t, "claim bounded batch", err)
	if len(batch.Matches) == 0 || len(batch.Matches) >= MaxBufferedMatches {
		t.Fatalf("expected bounded partial batch, got %d matches", len(batch.Matches))
	}
	totalBytes := 0
	for _, m := range batch.Matches {
		totalBytes += len(m.Evidence)
	}
	if totalBytes > MaxBatchBytes {
		t.Fatalf("batch bytes %d exceeded %d", totalBytes, MaxBatchBytes)
	}
}

func TestValidationRejectionsAndRestrictedEvidence(t *testing.T) {
	store, clock, _, owner := openTestWatchStore(t)

	// Repeat on terminal-only process_done is rejected with WATCH_REPEAT_UNSUPPORTED.
	_, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-1"}}},
	})
	var valErr *ValidationError
	if !errors.As(err, &valErr) || valErr.Code != "WATCH_REPEAT_UNSUPPORTED" {
		t.Fatalf("expected WATCH_REPEAT_UNSUPPORTED, got %v", err)
	}

	// Expiry out of range is rejected with WATCH_EXPIRY_INVALID.
	_, err = store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		ExpiresAt:  clock.Now().Add(10 * time.Second),
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-1"}}},
	})
	if !errors.As(err, &valErr) || valErr.Code != "WATCH_EXPIRY_INVALID" {
		t.Fatalf("expected WATCH_EXPIRY_INVALID, got %v", err)
	}

	// Worker job cannot request delivery: wake.
	workerOwner := Owner{ProjectID: owner.ProjectID, SessionID: owner.SessionID, WorkerJobID: "job-missing"}
	_, err = store.Register(t.Context(), RegisterRequest{
		Owner:      workerOwner,
		Delivery:   DeliveryWake,
		Conditions: []awaitstore.Condition{{Kind: "port_ready", Port: 3000}},
	})
	if !errors.As(err, &valErr) || valErr.Code != "WATCH_WAKE_NOT_ALLOWED" {
		t.Fatalf("expected WATCH_WAKE_NOT_ALLOWED, got %v", err)
	}

	// Restricted match redacts inline evidence and retains a reference.
	rcpt, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-secret"}}},
	})
	testutil.FailErr(t, "register secret watch", err)
	m, ok, err := store.RecordMatch(t.Context(), rcpt.WatchID, MatchInput{
		SourceKind:   ConditionProcessDone,
		OccurrenceID: "occ-restricted",
		Outcome:      "satisfied",
		Restricted:   true,
		Evidence:     map[string]any{"secret": "never-inline"},
	}, nil)
	testutil.FailErr(t, "record restricted match", err)
	if !ok || !m.Restricted || strings.Contains(string(m.Evidence), "never-inline") || m.RetainedRef == "" {
		t.Fatalf("restricted match leaked or lost ref: %+v", m)
	}

	// SetDelivery on a terminal watch returns TerminalError.
	_, err = store.SetDelivery(t.Context(), owner, rcpt.Handle, DeliveryWake)
	var termErr *TerminalError
	if !errors.As(err, &termErr) {
		t.Fatalf("expected TerminalError on completed one-shot, got %v", err)
	}
}
