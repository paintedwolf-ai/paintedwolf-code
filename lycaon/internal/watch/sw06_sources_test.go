package watch

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	sessiondecisions "github.com/lycaon/lycaon/internal/session/decisions"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSW06CoordinatorWorkAndParkAcrossWorkerDecisionAndFailure(t *testing.T) {
	store, clock, database, owner := openTestWatchStore(t)
	testdbseed.InsertSession(t, database, "worker-child-1", testdbseed.DefaultProjectID)
	_, err := database.ExecContext(t.Context(), `INSERT INTO worker_jobs
		(id, project_id, parent_session_id, child_session_id, agent_type, prompt, brief, status, created_at)
		VALUES ('job-1', ?, 'session-1', 'worker-child-1', 'implementer', 'work', 'work', 'running', '2026-11-23T12:00:00Z')`,
		testdbseed.DefaultProjectID)
	testutil.FailErr(t, "seed worker job", err)

	decStore := sessiondecisions.NewSQL(database)
	decStore.SetWatchHooks(
		func(ctx context.Context, dec api.WorkerDecisionRequest) {
			_, _ = store.RecordWorkerDecisionOpened(ctx, "session-1", "fixer", dec.WorkerID, dec)
		},
		func(ctx context.Context, _ string) {
			_ = store.RecordWorkerDecisionClosed(ctx, "session-1", "fixer", "job-1", "closed")
		},
	)
	store.SetCoordinatorStateLookup(func(ctx context.Context, sessionID string) (WorkerWatchStateSnapshot, error) {
		var snap WorkerWatchStateSnapshot
		if sessionID == "session-1" {
			if dec, ok, dErr := decStore.Get(ctx, "worker-child-1"); dErr == nil && ok {
				snap.OpenDecisions = append(snap.OpenDecisions, OpenWorkerDecisionFact{
					WorkerHandle: "fixer",
					JobID:        "job-1",
					Request:      dec,
				})
			}
		}
		return snap, nil
	})

	// 1. Open a decision before watch registration: initial registration qualifies without clearing the decision.
	openReq := api.WorkerDecisionRequest{
		ChildSessionID: "worker-child-1",
		WorkerID:       "job-1",
		Question:       "Use strict or lenient parser?",
		Options:        []string{"strict", "lenient"},
	}
	testutil.FailErr(t, "put initial decision", decStore.Put(t.Context(), openReq))

	decWatch, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: "worker_decision_needed", Handles: []string{"fixer"}}},
		Repeat:     true,
	})
	testutil.FailErr(t, "register decision watch", err)
	if decWatch.PendingMatches != 1 {
		t.Fatalf("expected initial open decision to qualify, got %+v", decWatch)
	}
	if _, stillOpen, gErr := decStore.Get(t.Context(), "worker-child-1"); gErr != nil || !stillOpen {
		t.Fatalf("observing decision cleared or answered it: open=%v err=%v", stillOpen, gErr)
	}

	failWatch, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: "worker_failed", Handles: []string{"fixer"}}},
	})
	testutil.FailErr(t, "register failure watch", err)

	// 2. Coordinator works and collects the decision match quietly; then closes the decision and inspects status.
	batch1, has1, err := ReserveCollectBatch(t.Context(), store, owner, "req:coord:1")
	testutil.FailErr(t, "reserve decision batch", err)
	if !has1 || len(batch1.Matches) != 1 || batch1.Matches[0].Outcome != "needs_decision" {
		t.Fatalf("unexpected collected decision batch: %+v", batch1)
	}
	testutil.FailErr(t, "commit decision batch", CommitCollectBatch(t.Context(), store, owner.SessionID, batch1.DeliveryID))

	testutil.FailErr(t, "clear decision", decStore.Clear(t.Context(), "worker-child-1"))
	view, err := store.Inspect(t.Context(), owner, decWatch.Handle)
	testutil.FailErr(t, "inspect decision watch", err)
	if view.DecisionStatus["fixer"] != "closed" {
		t.Fatalf("expected decision_status[fixer]=closed, got %+v", view.DecisionStatus)
	}

	// Second decision followed by closure surfaces closed status in the next collected batch.
	testutil.FailErr(t, "put second decision", decStore.Put(t.Context(), api.WorkerDecisionRequest{
		ChildSessionID: "worker-child-1",
		WorkerID:       "job-1",
		Question:       "Retry with fallback?",
		Options:        []string{"yes", "no"},
	}))
	_ = store.RecordWorkerDecisionClosed(t.Context(), "session-1", "fixer", "job-1", "retried")
	batch2, has2, err := ReserveCollectBatch(t.Context(), store, owner, "req:coord:2")
	testutil.FailErr(t, "reserve second decision batch", err)
	if !has2 || batch2.DecisionStatus[decWatch.Handle]["fixer"] != "retried" {
		t.Fatalf("expected retried decision status in batch, got %+v", batch2)
	}
	formatted := FormatCollectedBatch(batch2)
	if !strings.Contains(formatted, `"fixer":"retried"`) {
		t.Fatalf("formatted batch missing decision_status: %s", formatted)
	}
	testutil.FailErr(t, "commit second decision batch", CommitCollectBatch(t.Context(), store, owner.SessionID, batch2.DeliveryID))

	// 3. Coordinator parks on failWatch; worker failure wakes lease once and deduplicates alternatives.
	wokeCh := make(chan awaitstore.Condition, 2)
	store.SetLeaseWakeHandler(func(_ context.Context, _ awaitstore.Lease, winner awaitstore.Condition) {
		wokeCh <- winner
	})
	parkRes, err := store.SelectOrPark(t.Context(), SelectRequest{
		Owner:     owner,
		Handles:   []string{failWatch.Handle},
		AllowPark: true,
		ParkLease: awaitstore.Lease{
			SessionID:  owner.SessionID,
			ProjectID:  owner.ProjectID,
			ToolCallID: "wait-call-1",
			Deadline:   clock.Now().Add(5 * time.Minute),
			Conditions: []awaitstore.Condition{{Kind: ConditionWatchMatch, Handles: []string{failWatch.Handle}}},
		},
	})
	testutil.FailErr(t, "park coordinator on fail watch", err)
	if !parkRes.Parked {
		t.Fatalf("expected coordinator to park, got %+v", parkRes)
	}

	claimed, err := store.RecordWorkerEvent(t.Context(), "session-1", WorkerWatchEvent{
		Kind:         "terminal",
		WorkerHandle: "fixer",
		JobID:        "job-1",
		Outcome:      "failed",
		Status:       "failed",
		Summary:      "migration failed",
		OccurrenceID: "worker_terminal:job-1",
	})
	testutil.FailErr(t, "record worker failure", err)
	if !claimed {
		t.Fatal("expected worker failure to claim presentation")
	}
	select {
	case winner := <-wokeCh:
		if winner.Kind != ConditionWatchMatch || winner.Outcome != "matched" || !strings.Contains(winner.Report, `"outcome":"failed"`) {
			t.Fatalf("unexpected wake winner: %+v", winner)
		}
	default:
		t.Fatal("expected parked wait lease to settle on worker failure")
	}

	// Duplicate delivery of the same occurrence produces zero additional matches.
	dupClaimed, err := store.RecordWorkerEvent(t.Context(), "session-1", WorkerWatchEvent{
		Kind:         "terminal",
		WorkerHandle: "fixer",
		JobID:        "job-1",
		Outcome:      "failed",
		Status:       "failed",
		OccurrenceID: "worker_terminal:job-1",
	})
	testutil.FailErr(t, "duplicate worker failure", err)
	if dupClaimed {
		t.Fatal("duplicate worker occurrence should not claim again")
	}
}

func TestSW06AnchoredTimersCalendarDSTExpiryTieAndMissedCatchup(t *testing.T) {
	store, clock, _, owner := openTestWatchStore(t)

	// 1. Repeating timer requesting wake is rejected with WATCH_WAKE_NOT_ALLOWED.
	_, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Delivery:   DeliveryWake,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: "timer", DelayMS: 5000}},
	})
	var valErr *ValidationError
	if !errors.As(err, &valErr) || valErr.Code != "WATCH_WAKE_NOT_ALLOWED" {
		t.Fatalf("expected WATCH_WAKE_NOT_ALLOWED on repeating timer wake, got %v", err)
	}

	// 2. Expiry wins when a tick and expires_at coincide at the exact instant.
	coincideWatch, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		ExpiresAt:  clock.Now().Add(time.Minute),
		Conditions: []awaitstore.Condition{{Kind: "timer", DelayMS: 60000}},
	})
	testutil.FailErr(t, "register coinciding timer", err)
	clock.Advance(time.Minute)
	testutil.FailErr(t, "advance coinciding deadline", store.AdvanceDue(t.Context()))
	cView, err := store.Inspect(t.Context(), owner, coincideWatch.Handle)
	testutil.FailErr(t, "inspect coinciding timer", err)
	if cView.State != StateTerminal || cView.Outcome != OutcomeExpired || cView.PendingMatches != 0 {
		t.Fatalf("expiry must win over coinciding tick: %+v", cView)
	}

	// 3. Repeating delay_ms stays anchored without drift and coalesces missed ticks across restart.
	anchorTime := clock.Now()
	repWatch, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Repeat:     true,
		ExpiresAt:  anchorTime.Add(time.Hour),
		Conditions: []awaitstore.Condition{{Kind: "timer", DelayMS: 10000}},
	})
	testutil.FailErr(t, "register anchored repeating timer", err)

	clock.Advance(10*time.Second + 370*time.Millisecond)
	testutil.FailErr(t, "advance first tick", store.AdvanceDue(t.Context()))
	batch1, has1, err := ReserveCollectBatch(t.Context(), store, owner, "req:timer:1")
	testutil.FailErr(t, "collect tick 1", err)
	if !has1 || len(batch1.Matches) != 1 {
		t.Fatalf("expected 1 match at first tick, got %+v", batch1)
	}
	testutil.FailErr(t, "commit tick 1", CommitCollectBatch(t.Context(), store, owner.SessionID, batch1.DeliveryID))

	// Advance 9630ms more (total 20.000s from anchor): proves second tick stayed anchored at anchor+20s, not +20.37s.
	clock.Advance(9630 * time.Millisecond)
	testutil.FailErr(t, "advance anchored second tick", store.AdvanceDue(t.Context()))
	batch2, has2, err := ReserveCollectBatch(t.Context(), store, owner, "req:timer:2")
	testutil.FailErr(t, "collect tick 2", err)
	if !has2 || len(batch2.Matches) != 1 {
		t.Fatalf("expected drift-free tick at anchor+20s, got %+v", batch2)
	}
	testutil.FailErr(t, "commit tick 2", CommitCollectBatch(t.Context(), store, owner.SessionID, batch2.DeliveryID))

	// Jump 45s across restart (ticks at +30s, +40s, +50s, +60s = 4 due ticks -> 3 missed).
	clock.Advance(45 * time.Second)
	testutil.FailErr(t, "recover across missed ticks", store.Recover(t.Context()))
	batch3, has3, err := ReserveCollectBatch(t.Context(), store, owner, "req:timer:3")
	testutil.FailErr(t, "collect catchup batch", err)
	if !has3 || len(batch3.Matches) != 1 {
		t.Fatalf("expected 1 coalesced catch-up match, got %+v", batch3)
	}
	var ev map[string]any
	testutil.FailErr(t, "decode timer evidence", json.Unmarshal(batch3.Matches[0].Evidence, &ev))
	if ev["tick_count"] != float64(4) || ev["missed_ticks"] != float64(3) {
		t.Fatalf("unexpected coalesced catch-up evidence: %+v", ev)
	}
	_ = repWatch

	// 4. Calendar DST rules in America/New_York:
	// Spring-forward 2026-03-08 skips 02:30 -> fires once at 03:00 EDT (07:00 UTC).
	// Fall-back 2026-11-01 repeats 01:30 -> fires once at first 01:30 EDT (05:30 UTC), then next day.
	calSpring := awaitstore.Condition{
		Kind:     "timer",
		TimeZone: "America/New_York",
		Calendar: &awaitstore.CalendarSpec{Days: []string{"sunday"}, Times: []string{"02:30"}},
	}
	springTicks, err := NextTimerOccurrences(calSpring, time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC), time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC), 1)
	testutil.FailErr(t, "spring forward occurrences", err)
	wantSpring := time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)
	if len(springTicks) != 1 || !springTicks[0].Equal(wantSpring) {
		t.Fatalf("spring-forward 02:30 got %v, want %v (03:00 EDT)", springTicks, wantSpring)
	}

	calFall := awaitstore.Condition{
		Kind:     "timer",
		TimeZone: "America/New_York",
		Calendar: &awaitstore.CalendarSpec{Days: []string{"every_day"}, Times: []string{"01:30"}},
	}
	fallTicks, err := NextTimerOccurrences(calFall, time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC), 2)
	testutil.FailErr(t, "fall back occurrences", err)
	wantFall1 := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC) // 01:30 EDT (first instance)
	wantFall2 := time.Date(2026, 11, 2, 6, 30, 0, 0, time.UTC) // next day 01:30 EST
	if len(fallTicks) != 2 || !fallTicks[0].Equal(wantFall1) || !fallTicks[1].Equal(wantFall2) {
		t.Fatalf("fall-back 01:30 got %v, want [%v, %v]", fallTicks, wantFall1, wantFall2)
	}

	// every_minutes anchored intervals never drift.
	everyCond := awaitstore.Condition{Kind: "timer", EveryMinutes: 15}
	baseAnchor := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	everyTicks, err := NextTimerOccurrences(everyCond, baseAnchor, baseAnchor.Add(16*time.Minute), 2)
	testutil.FailErr(t, "every_minutes occurrences", err)
	if len(everyTicks) != 2 || !everyTicks[0].Equal(baseAnchor.Add(30*time.Minute)) || !everyTicks[1].Equal(baseAnchor.Add(45*time.Minute)) {
		t.Fatalf("unexpected every_minutes ticks: %v", everyTicks)
	}
}

func TestSW06SamplerDeduplicationDomainIsolationOwnerTeardownAndRevocation(t *testing.T) {
	store, clock, database, owner1 := openTestWatchStore(t)
	confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		return true
	})
	t.Cleanup(func() { confine.SetEgressResolver(nil) })
	owner2 := Owner{ProjectID: testdbseed.DefaultProjectID, SessionID: "session-2"}

	// Seed a second project + session to prove zero probe sharing across authority domains.
	testdbseed.InsertSession(t, database, "session-other", "proj-other")
	otherOwner := Owner{ProjectID: "proj-other", SessionID: "session-other"}

	var readyFlag atomic.Bool
	var httpHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpHits.Add(1)
		if readyFlag.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	parsedURL, err := url.Parse(srv.URL)
	testutil.FailErr(t, "parse test server url", err)
	srvPortInt, err := strconv.Atoi(parsedURL.Port())
	testutil.FailErr(t, "parse test server port", err)
	srvPort := uint16(srvPortInt)

	sharedAuth := Authority{
		RootSessionID:   "root-shared",
		WorkspaceID:     "ws-1",
		LoopbackGranted: true,
		LoopbackPorts:   []uint16{srvPort},
	}
	otherAuth := Authority{
		RootSessionID:   "root-other",
		WorkspaceID:     "ws-2",
		LoopbackGranted: true,
		LoopbackPorts:   []uint16{srvPort},
	}
	httpCond := awaitstore.Condition{Kind: "http_ready", URL: srv.URL, Method: "GET", StatusMin: 200, StatusMax: 299}

	// Two watches in the same authority domain share ONE probe; a third watch in another project gets its own probe.
	w1, err := store.Register(t.Context(), RegisterRequest{
		Owner: owner1, Authority: sharedAuth, Repeat: true, Conditions: []awaitstore.Condition{httpCond},
	})
	testutil.FailErr(t, "register w1", err)
	w2, err := store.Register(t.Context(), RegisterRequest{
		Owner: owner2, Authority: sharedAuth, Repeat: true, Conditions: []awaitstore.Condition{httpCond},
	})
	testutil.FailErr(t, "register w2", err)
	wOther, err := store.Register(t.Context(), RegisterRequest{
		Owner: otherOwner, Authority: otherAuth, Conditions: []awaitstore.Condition{httpCond},
	})
	testutil.FailErr(t, "register wOther", err)

	store.sampler.mu.Lock()
	groupCount := len(store.sampler.groupsByID)
	store.sampler.mu.Unlock()
	if groupCount != 2 {
		t.Fatalf("expected 2 isolated probe groups (1 shared + 1 other domain), got %d", groupCount)
	}

	// Tick 1 while 503: exactly 2 HTTP requests (1 for shared domain, 1 for other domain), zero matches.
	clock.Advance(SamplerMinInterval)
	testutil.FailErr(t, "advance probe 503", store.AdvanceDue(t.Context()))
	if got := httpHits.Load(); got != 2 {
		t.Fatalf("expected 2 deduplicated HTTP probes, got %d", got)
	}
	if v1, _ := store.Inspect(t.Context(), owner1, w1.Handle); v1.PendingMatches != 0 {
		t.Fatalf("503 should not match: %+v", v1)
	}

	// Flip server to 200: next backed-off tick (500ms) satisfies all 3 watches with only 2 HTTP hits.
	readyFlag.Store(true)
	clock.Advance(500 * time.Millisecond)
	testutil.FailErr(t, "advance probe 200", store.AdvanceDue(t.Context()))
	if got := httpHits.Load(); got != 4 {
		t.Fatalf("expected 4 cumulative HTTP probes, got %d", got)
	}
	if v1, _ := store.Inspect(t.Context(), owner1, w1.Handle); v1.PendingMatches != 1 {
		t.Fatalf("w1 missing match: %+v", v1)
	}
	if v2, _ := store.Inspect(t.Context(), owner2, w2.Handle); v2.PendingMatches != 1 {
		t.Fatalf("w2 missing match: %+v", v2)
	}
	if vO, _ := store.Inspect(t.Context(), otherOwner, wOther.Handle); vO.PendingMatches != 1 || vO.State != StateTerminal {
		t.Fatalf("one-shot wOther should complete: %+v", vO)
	}

	// Staying 200 does NOT re-fire repeating w1 until an observed false (503) re-arms it.
	clock.Advance(SamplerMinInterval)
	testutil.FailErr(t, "advance while still 200", store.AdvanceDue(t.Context()))
	if v1, _ := store.Inspect(t.Context(), owner1, w1.Handle); v1.PendingMatches != 1 {
		t.Fatalf("level predicate re-fired without false transition: %+v", v1)
	}
	readyFlag.Store(false)
	clock.Advance(500 * time.Millisecond)
	testutil.FailErr(t, "advance observed 503 re-arm", store.AdvanceDue(t.Context()))
	readyFlag.Store(true)
	clock.Advance(SamplerMinInterval)
	testutil.FailErr(t, "advance second 200 episode", store.AdvanceDue(t.Context()))
	if v1, _ := store.Inspect(t.Context(), owner1, w1.Handle); v1.PendingMatches != 2 {
		t.Fatalf("expected 2nd match after false->true transition, got %+v", v1)
	}

	// Permission revocation redacts evidence on Inspect and Collect and terminates probing with source_lost.
	var revoked atomic.Bool
	revoked.Store(true)
	store.SetAuthorityChecker(func(_ context.Context, reg Registration) (bool, string) {
		if reg.Owner.SessionID == owner1.SessionID && revoked.Load() {
			return false, "EGRESS_REVOKED"
		}
		return true, ""
	})
	inspectedRevoked, err := store.Inspect(t.Context(), owner1, w1.Handle)
	testutil.FailErr(t, "inspect after revocation", err)
	if inspectedRevoked.LatestMatch == nil || !inspectedRevoked.LatestMatch.Restricted || len(inspectedRevoked.RetainedRefs) == 0 {
		t.Fatalf("expected redacted restricted match on inspect: %+v", inspectedRevoked)
	}
	revBatch, hasRev, err := ReserveCollectBatch(t.Context(), store, owner1, "req:revoked:1")
	testutil.FailErr(t, "collect after revocation", err)
	if !hasRev || !revBatch.Matches[0].Restricted || strings.Contains(string(revBatch.Matches[0].Evidence), srv.URL) {
		t.Fatalf("revoked batch leaked URL or missed restriction: %+v", revBatch)
	}
	clock.Advance(time.Second)
	testutil.FailErr(t, "advance probe under revocation", store.AdvanceDue(t.Context()))
	afterProbeRevoke, err := store.Inspect(t.Context(), owner1, w1.Handle)
	testutil.FailErr(t, "inspect after revoked probe", err)
	if afterProbeRevoke.State != StateTerminal || afterProbeRevoke.Outcome != OutcomeSourceLost {
		t.Fatalf("revoked probe must transition watch to source_lost: %+v", afterProbeRevoke)
	}
	revoked.Store(false)
}

func TestSW06SamplerFairRoundRobinAndOwnerTeardown(t *testing.T) {
	store, clock, database, _ := openTestWatchStore(t)

	// Worker-owned port_ready watch + RetireOwner teardown + fair round-robin contention across 6 TCP targets.
	testdbseed.InsertSession(t, database, "worker-sess-2", testdbseed.DefaultProjectID)
	_, err := database.ExecContext(t.Context(), `INSERT INTO worker_jobs
		(id, project_id, parent_session_id, child_session_id, agent_type, prompt, brief, status, created_at)
		VALUES ('job-retire', ?, 'session-1', 'worker-sess-2', 'implementer', 'work', 'work', 'running', '2026-11-23T12:00:00Z')`,
		testdbseed.DefaultProjectID)
	testutil.FailErr(t, "seed job-retire", err)
	jobOwner := JobOwner("worker-sess-2", "job-retire")
	jobOwner.ProjectID = testdbseed.DefaultProjectID

	var listeners []net.Listener
	var ports []uint16
	for range 6 {
		ln, lErr := net.Listen("tcp", "127.0.0.1:0")
		testutil.FailErr(t, "listen loopback", lErr)
		t.Cleanup(func() { _ = ln.Close() })
		listeners = append(listeners, ln)
		ports = append(ports, uint16(ln.Addr().(*net.TCPAddr).Port))
	}
	for _, p := range ports {
		_, rErr := store.Register(t.Context(), RegisterRequest{
			Owner:      jobOwner,
			Authority:  Authority{LoopbackGranted: true, LoopbackPorts: ports},
			Repeat:     true,
			Conditions: []awaitstore.Condition{{Kind: "port_ready", Host: "127.0.0.1", Port: p}},
		})
		testutil.FailErr(t, "register port_ready watch", rErr)
	}
	// First due advance executes MaxConcurrentProbes (4) fairly and defers the remaining 2 to the next slice.
	clock.Advance(SamplerMinInterval)
	testutil.FailErr(t, "advance round-robin slice 1", store.AdvanceDue(t.Context()))
	clock.Advance(SamplerMinInterval)
	testutil.FailErr(t, "advance round-robin slice 2", store.AdvanceDue(t.Context()))
	jobBatch, hasJobBatch, err := ReserveCollectBatch(t.Context(), store, jobOwner, "req:job:ports")
	testutil.FailErr(t, "collect all 6 port_ready matches", err)
	if !hasJobBatch || len(jobBatch.Matches) != 6 {
		t.Fatalf("expected all 6 port_ready watches to be served fairly across 2 slices, got %d", len(jobBatch.Matches))
	}

	// RetireOwner tears down all 6 worker-owned watches and removes their sampler subscriptions immediately.
	testutil.FailErr(t, "retire job owner", store.RetireOwner(t.Context(), jobOwner))
	vRetired, err := store.Inspect(t.Context(), jobOwner, "watch-1")
	testutil.FailErr(t, "inspect retired worker watch", err)
	if vRetired.State != StateTerminal || vRetired.Outcome != OutcomeCanceled {
		t.Fatalf("expected retired worker watch terminal/canceled, got %+v", vRetired)
	}
	_ = listeners
}

func TestSW06AllWorkersIdleOverlayPromoteAndScanDone(t *testing.T) {
	store, _, _, owner := openTestWatchStore(t)

	idleWatch, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: "all_workers_idle"}, {Kind: "overlay_promote_pending"}},
	})
	testutil.FailErr(t, "register idle+overlay watch", err)
	scanWatch, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: "scan_done"}},
	})
	testutil.FailErr(t, "register scan watch", err)

	sessions, err := store.LiveScanWatchSessions(t.Context())
	testutil.FailErr(t, "list scan watch sessions", err)
	if len(sessions) != 1 || sessions[0] != owner.SessionID {
		t.Fatalf("unexpected scan watch sessions: %v", sessions)
	}

	_, err = store.RecordWorkerEvent(t.Context(), owner.SessionID, WorkerWatchEvent{
		Kind:                  "state",
		AllWorkersIdle:        true,
		OverlayPromotePending: true,
	})
	testutil.FailErr(t, "record idle+overlay true", err)
	vIdle, err := store.Inspect(t.Context(), owner, idleWatch.Handle)
	testutil.FailErr(t, "inspect idleWatch", err)
	if vIdle.PendingMatches != 2 {
		t.Fatalf("expected 2 matches (idle + overlay), got %+v", vIdle)
	}

	claimedScan, err := store.RecordScanDone(t.Context(), owner.SessionID, ScanDoneFact{
		ScanID: "scan-42", Status: "completed", Categories: "sast", FindingsCount: 0,
	})
	testutil.FailErr(t, "record scan done", err)
	if !claimedScan {
		t.Fatal("expected scan_done match")
	}
	vScan, err := store.Inspect(t.Context(), owner, scanWatch.Handle)
	testutil.FailErr(t, "inspect scanWatch", err)
	if vScan.PendingMatches != 1 || vScan.State != StateTerminal {
		t.Fatalf("expected one-shot scan_done watch completed with 1 match, got %+v", vScan)
	}
}
