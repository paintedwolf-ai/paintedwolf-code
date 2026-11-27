package loopwake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// TestWW02C01LeaseInstallationReleaseInProcess exercises C01 exit-17 completion
// injected at the exact mid-lease-installation boundary (after store.Arm and sleep
// arming, before wait() finishes returning) using real SQLite storage and LoopEngine.
func TestWW02C01LeaseInstallationReleaseInProcess(t *testing.T) {
	loop, store, reg := completionWaitFixture(t)

	const (
		sessionID = "s1"
		handle    = "command-1"
		exitCode  = 17
		report    = "handle=command-1 termination=exited exit_code=17\nww02-c01-inprocess:stdout:ok"
	)

	var (
		seq                atomic.Int64
		processRunning     atomic.Bool
		reportPublished    atomic.Bool
		reportPublishedSeq atomic.Int64
		deliverySeq        atomic.Int64
		barrierReached     = make(chan struct{})
		barrierRelease     = make(chan struct{})
		reachOnce          sync.Once
		mu                 sync.Mutex
		deliveries         []awaitstore.Condition
		leaseEdges         []WaitLease
	)
	processRunning.Store(true)

	deps := loopDepsSnapshotForTest(loop)
	deps.ProcessRunning = func(string, []string) bool { return processRunning.Load() }
	deps.ProcessState = func(sid, h string) (bool, bool) {
		return sid == sessionID && h == handle, processRunning.Load()
	}
	deps.ProcessReport = func(sid, h string) (string, bool) {
		if sid == sessionID && h == handle && reportPublished.Load() {
			return report, true
		}
		return "", false
	}
	deps.PublishWaitLease = func(_ context.Context, _ string, lease WaitLease) {
		mu.Lock()
		leaseEdges = append(leaseEdges, lease)
		mu.Unlock()
		// Inject barrier mid-lease-installation when the active lease edge is published.
		if lease.Active {
			reachOnce.Do(func() {
				close(barrierReached)
				<-barrierRelease
			})
		}
	}
	deps.RunWaitResume = func(_ context.Context, _ string, delivery WaitDelivery) (*promptresult.Result, error) {
		if err := delivery.Admitted(); err != nil {
			return nil, err
		}
		dSeq := seq.Add(1)
		deliverySeq.CompareAndSwap(0, dSeq)
		mu.Lock()
		deliveries = append(deliveries, delivery.Condition)
		mu.Unlock()
		return &promptresult.Result{}, nil
	}
	loop.SetDeps(deps)

	// Start wait(process_done, handles=[command-1]) in a goroutine; it will pause inside PublishWaitLease.
	waitDone := make(chan error, 1)
	go func() {
		_, err := reg.Run(t.Context(), "wait", completionWaitArgs(), tools.ToolContext{
			Identity: tools.InvocationIdentity{
				SessionID:  sessionID,
				ProjectID:  testdbseed.DefaultProjectID,
				ToolCallID: "ww02-wait-call-1",
			},
		})
		waitDone <- err
	}()

	// 1. Verify mid-lease-installation barrier is reached.
	select {
	case <-barrierReached:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for mid-lease-installation barrier")
	}

	// Confirm the durable lease is already armed in SQLite at this boundary.
	armedDuringInstall, err := store.Active(t.Context())
	testutil.FailErr(t, "read active leases during install", err)
	if len(armedDuringInstall) != 1 || armedDuringInstall[0].SessionID != sessionID {
		close(barrierRelease)
		t.Fatalf("active leases at mid-installation = %+v, want 1 armed lease for %s", armedDuringInstall, sessionID)
	}

	// 2. Release exit-17 completion + report publication AT the mid-lease-installation boundary.
	processRunning.Store(false)
	reportPublished.Store(true)
	pubSeq := seq.Add(1)
	reportPublishedSeq.Store(pubSeq)
	loop.Nudges.NudgeProcessFinished(
		t.Context(),
		sessionID,
		handle,
		anchor.Envelope{CommandCompletionDigest: report},
	)

	// 3. Unblock lease installation so wait() finishes returning.
	close(barrierRelease)
	select {
	case err := <-waitDone:
		testutil.FailErr(t, "wait() returned error", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for wait() to return after barrier release")
	}

	// 4. Wait for terminal receipt delivery and drain async turns.
	testutil.WaitFor(t, "terminal receipt delivered", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(deliveries) >= 1
	})
	loop.Turns.WaitForAsyncTurns(t.Context())

	// 5. Assert all WW02 C01 invariants.
	activeAfter, err := store.Active(t.Context())
	testutil.FailErr(t, "read active leases after delivery", err)
	if len(activeAfter) != 0 {
		t.Fatalf("leaked active wait_leases after delivery: %+v", activeAfter)
	}
	if loop.Waits.IsSleeping(sessionID) {
		t.Fatal("session still sleeping after terminal exit-17 delivery")
	}

	mu.Lock()
	gotDeliveries := append([]awaitstore.Condition(nil), deliveries...)
	gotEdgeCount := len(leaseEdges)
	mu.Unlock()

	if len(gotDeliveries) != 1 {
		t.Fatalf("delivery count = %d, want exactly 1 (%+v)", len(gotDeliveries), gotDeliveries)
	}
	winner := gotDeliveries[0]
	if winner.Outcome != "satisfied" {
		t.Fatalf("winner.Outcome = %q, want satisfied", winner.Outcome)
	}
	if len(winner.Handles) != 1 || winner.Handles[0] != handle {
		t.Fatalf("winner.Handles = %v, want [%s]", winner.Handles, handle)
	}
	if !strings.Contains(winner.Report, "exit_code=17") || winner.Report != report {
		t.Fatalf("winner.Report = %q, want exact exit-17 report %q", winner.Report, report)
	}
	if dSeq := deliverySeq.Load(); dSeq <= pubSeq {
		t.Fatalf("publication-before-delivery violated: delivery_seq=%d <= report_published_seq=%d", dSeq, pubSeq)
	}

	// 6. Optional: write versioned checker-compatible JSON evidence if WW_EVIDENCE_OUT is set.
	if outPath := strings.TrimSpace(os.Getenv("WW_EVIDENCE_OUT")); outPath != "" {
		reportSum := sha256.Sum256([]byte(report))
		reportSHA := hex.EncodeToString(reportSum[:])
		bundle := map[string]any{
			"schema_version": 1,
			"run_id":         "inprocess-c01-lease-installation-release",
			"lane":           "in-process",
			"clock_domain":   "manual",
			"case_id":        "c01-lease-installation-release",
			"expectations": map[string]any{
				"expected_outcome":          "delivered",
				"owed_recipients":           []string{"session:" + sessionID},
				"owed_occurrences":          []string{"occ-c01-exit-17"},
				"expected_attempt":          1,
				"expected_invocation_id":    "ww02-wait-call-1",
				"expected_revision":         1,
				"expected_exit_code":        exitCode,
				"expected_report_sha256":    reportSHA,
				"report_published_seq":      pubSeq,
				"require_zero_armed_leases": true,
			},
			"observers": map[string]any{
				"source": map[string]any{
					"observer":     "source",
					"clock_domain": "manual",
					"facts": map[string]any{
						"expected_exit_code":      exitCode,
						"report_published_seq":    pubSeq,
						"expected_payload_sha256": reportSHA,
					},
				},
				"storage": map[string]any{
					"observer":          "storage",
					"clock_domain":      "manual",
					"available":         true,
					"armed_lease_count": len(activeAfter),
					"lease_edge_count":  gotEdgeCount,
				},
				"admission": map[string]any{
					"observer":     "admission",
					"clock_domain": "manual",
					"executions":   []any{map[string]any{"session_id": sessionID, "settled": true}},
				},
				"rendered_input": map[string]any{
					"observer":     "rendered_input",
					"clock_domain": "manual",
					"presentations": []any{
						map[string]any{
							"event_seq":         deliverySeq.Load(),
							"turn":              1,
							"clock_domain":      "manual",
							"presentation_kind": "new_model_presentation",
							"session_id":        sessionID,
							"destination_scope": "session:" + sessionID,
							"handle":            handle,
							"invocation_id":     "ww02-wait-call-1",
							"occurrence_id":     "occ-c01-exit-17",
							"claim_id":          "claim-c01-inprocess",
							"attempt":           1,
							"revision":          1,
							"exit_code":         exitCode,
							"terminal":          true,
							"report":            winner.Report,
							"report_readable":   winner.Report != "",
							"report_sha256":     reportSHA,
						},
					},
				},
				"den": map[string]any{"observer": "den", "status": "not_run"},
			},
		}
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			t.Fatalf("mkdir WW_EVIDENCE_OUT dir: %v", err)
		}
		raw, err := json.MarshalIndent(bundle, "", "  ")
		testutil.FailErr(t, "marshal in-process evidence", err)
		if err := os.WriteFile(outPath, append(raw, '\n'), 0o644); err != nil {
			t.Fatalf("write WW_EVIDENCE_OUT: %v", err)
		}
	}
}
