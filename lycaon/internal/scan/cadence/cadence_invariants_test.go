package cadence

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestInvariantClaimTokenConservationAndDesireSettlement(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	seedNonEmptyProject(t, dir)
	testutil.FailErr(t, "attach", cadence.BaselineRoot(t.Context(), dir))
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "canonical path", err)

	completeCadenceScans(t, cadence, store)

	cfg := cadenceCfg(cadence)
	testutil.FailErr(t, "note writes", cadence.NoteWrites(t.Context(), dir, []string{"main.go"}))
	clock.Advance(cfg.WriteBurstSettle() + time.Second)

	book := claimBook{c: cadence}
	claimed, err := book.claimDueSeries(t.Context(), canonical)
	testutil.FailErr(t, "claim due series", err)
	if len(claimed) == 0 {
		t.Fatal("expected due series to be claimed after write settle window")
	}
	for _, row := range claimed {
		if row.DispatchToken == "" || row.ClaimHeartbeatAt.IsZero() {
			t.Fatalf("claimed series %s missing lease: %+v", row.ScannerID, row)
		}
	}

	book.requeueDispatches(t.Context(), claimed)
	rowsAfterRequeue, err := store.ListSeriesForPath(t.Context(), canonical)
	testutil.FailErr(t, "series after requeue", err)
	for _, row := range rowsAfterRequeue {
		if row.DispatchToken != "" || !row.ClaimHeartbeatAt.IsZero() {
			t.Fatalf("requeued series %s kept lease: %+v", row.ScannerID, row)
		}
		if len(row.DesiredPaths) != 1 || row.DesiredPaths[0] != "main.go" || row.DesiredTrigger != api.ScanTriggerWriteBurst {
			t.Fatalf("requeued series %s lost desire: %+v", row.ScannerID, row)
		}
	}

	// Expire a claim lease via TTL and verify claimDueSeries recovers frozen desire.
	clock.Advance(cfg.WriteBurstSettle() + time.Second)
	claimedAgain, err := book.claimDueSeries(t.Context(), canonical)
	testutil.FailErr(t, "reclaim due series", err)
	if len(claimedAgain) != len(claimed) {
		t.Fatalf("reclaimed %d series, want %d", len(claimedAgain), len(claimed))
	}
	clock.Advance(scanbase.DispatchClaimTTL + time.Second)
	recovered, err := book.claimDueSeries(t.Context(), canonical)
	testutil.FailErr(t, "recover expired claims", err)
	if len(recovered) != len(claimed) {
		t.Fatalf("recovered %d series after TTL, want %d", len(recovered), len(claimed))
	}
	book.requeueDispatches(t.Context(), recovered)

	// Advance past due time, dispatch via Tick, complete scans, and verify all leases and desires settle.
	clock.Advance(cfg.WriteBurstSettle() + time.Second)
	cadence.Tick(t.Context())
	completeCadenceScans(t, cadence, store)
	settled, err := store.ListSeriesForPath(t.Context(), canonical)
	testutil.FailErr(t, "series after terminal settlement", err)
	for _, row := range settled {
		if row.DispatchToken != "" || !row.ClaimHeartbeatAt.IsZero() || len(row.DesiredPaths) != 0 || row.DesiredTrigger != "" || row.DesiredPassID != "" || !row.DueAt.IsZero() {
			t.Fatalf("settled series %s retained lease or desire: %+v", row.ScannerID, row)
		}
	}
}

func TestInvariantTriggerRankMonotonicityAndDeferralCap(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	seedNonEmptyProject(t, dir)
	testutil.FailErr(t, "attach", cadence.BaselineRoot(t.Context(), dir))
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "canonical path", err)
	completeCadenceScans(t, cadence, store)

	cfg := cadenceCfg(cadence)
	t0 := clock.Now().UTC()
	testutil.FailErr(t, "initial write", cadence.NoteWrites(t.Context(), dir, []string{"main.go"}))
	rows, err := store.ListSeriesForPath(t.Context(), canonical)
	testutil.FailErr(t, "series after first write", err)
	wantMaxDue := t0.Add(cfg.MaxDefer())
	for _, row := range rows {
		if !row.MaxDueAt.Equal(wantMaxDue) {
			t.Fatalf("initial MaxDueAt = %s, want %s", row.MaxDueAt, wantMaxDue)
		}
		if row.DesiredTrigger != api.ScanTriggerWriteBurst {
			t.Fatalf("initial trigger = %s, want write_burst", row.DesiredTrigger)
		}
	}

	// Repeated writes advance DueAt up to MaxDueAt, never beyond it.
	for i := 0; i < 8; i++ {
		clock.Advance(cfg.WriteBurstSettle() / 2)
		testutil.FailErr(t, "repeated write", cadence.NoteWrites(t.Context(), dir, []string{"main.go"}))
		rows, err = store.ListSeriesForPath(t.Context(), canonical)
		testutil.FailErr(t, "series after repeated write", err)
		for _, row := range rows {
			if !row.MaxDueAt.Equal(wantMaxDue) {
				t.Fatalf("MaxDueAt shifted to %s, want %s", row.MaxDueAt, wantMaxDue)
			}
			if row.DueAt.After(row.MaxDueAt) {
				t.Fatalf("DueAt %s exceeded MaxDueAt %s", row.DueAt, row.MaxDueAt)
			}
		}
	}

	// Whole-tree drift via NoteTree promotes trigger rank to authority_refresh; subsequent write_burst never demotes it.
	testutil.FailErr(t, "baseline tree count", cadence.NoteTree(t.Context(), dir, 10))
	testutil.FailErr(t, " drifted tree count", cadence.NoteTree(t.Context(), dir, 200))
	rows, err = store.ListSeriesForPath(t.Context(), canonical)
	testutil.FailErr(t, "series after tree drift", err)
	for _, row := range rows {
		if triggerRank(row.DesiredTrigger) < triggerRank(api.ScanTriggerAuthorityRefresh) {
			t.Fatalf("trigger after tree drift = %s, want >= authority_refresh", row.DesiredTrigger)
		}
	}

	testutil.FailErr(t, "modify file", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() { println(1) }\n"), 0o644))
	testutil.FailErr(t, "write after drift", cadence.NoteWrites(t.Context(), dir, []string{"main.go"}))
	rows, err = store.ListSeriesForPath(t.Context(), canonical)
	testutil.FailErr(t, "series after write following drift", err)
	for _, row := range rows {
		if triggerRank(row.DesiredTrigger) < triggerRank(api.ScanTriggerAuthorityRefresh) {
			t.Fatalf("write_burst demoted trigger to %s", row.DesiredTrigger)
		}
	}
}

func TestInvariantRetiredRootFencingAndReattachment(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	seedNonEmptyProject(t, dir)
	testutil.FailErr(t, "attach", cadence.BaselineRoot(t.Context(), dir))
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "canonical path", err)
	completeCadenceScans(t, cadence, store)

	dispatchCtx, finish, ok := cadence.roots.beginDispatch(t.Context(), &cadence.mu, canonical, "token-1")
	if !ok || finish == nil {
		t.Fatal("expected beginDispatch to succeed before retirement")
	}
	defer finish()

	testutil.FailErr(t, "retire root", cadence.RetireRoot(t.Context(), dir, nil))
	if dispatchCtx.Err() == nil {
		t.Fatal("RetireRoot did not cancel in-flight dispatch context")
	}
	if _, done, admitted := cadence.roots.beginDispatch(t.Context(), &cadence.mu, canonical, "token-2"); admitted || done != nil {
		t.Fatal("beginDispatch succeeded on a retired root")
	}
	claimed, err := (claimBook{c: cadence}).claimDueSeries(t.Context(), canonical)
	testutil.FailErr(t, "claim on retired root", err)
	if len(claimed) != 0 {
		t.Fatalf("claimDueSeries on retired root returned %d rows, want 0", len(claimed))
	}

	cadence.RootAttached(canonical)
	if cadence.roots.isRetired(canonical) {
		t.Fatal("RootAttached did not clear retirement fence")
	}
	_, finishReattached, admitted := cadence.roots.beginDispatch(t.Context(), &cadence.mu, canonical, "token-3")
	if !admitted || finishReattached == nil {
		t.Fatal("beginDispatch failed after RootAttached")
	}
	finishReattached()
}
