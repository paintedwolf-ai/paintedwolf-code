package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
)

func openWW04ManualStore(t *testing.T, start time.Time) (*Store, *ww02ManualClock, Owner) {
	t.Helper()
	database := testdbfixture.Open(t, "ww04.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, database, "session-2", testdbseed.DefaultProjectID)
	clk := newWW02ManualClock(start)
	store := NewStore(database, clk)
	owner := Owner{ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1"}
	return store, clk, owner
}

func newWW04ToolRegistry(t *testing.T, store *Store) (*tools.DefaultRegistry, string) {
	t.Helper()
	schemas, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "LoadSchemaDir", err)
	reg, err := tools.NewCatalogRegistry(schemas)
	testutil.FailErr(t, "NewCatalogRegistry", err)

	rootDir := t.TempDir()
	testutil.FailErr(t, "mkdir .git", os.MkdirAll(filepath.Join(rootDir, ".git", "refs", "heads"), 0o755))
	testutil.FailErr(t, "write .git/HEAD", os.WriteFile(filepath.Join(rootDir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))

	testutil.FailErr(t, "RegisterWatchTools", RegisterWatchTools(reg, ToolDeps{
		Store: store,
		ProfileConditions: map[string]map[string]bool{
			"coordinator": {
				"next_worker_done": true, "all_workers_idle": true, "overlay_promote_pending": true,
				"worker_decision_needed": true, "worker_failed": true, "scan_done": true,
				"process_done": true, "process_output": true, "http_ready": true, "port_ready": true,
				"file_change": true, "git_ref": true, "timer": true,
			},
			"implement": {
				"process_done": true, "process_output": true, "http_ready": true, "port_ready": true,
				"file_change": true, "git_ref": true, "timer": true,
			},
			"web_research": {
				"http_ready": true, "port_ready": true, "timer": true,
			},
		},
		WorkerKnown:  func(_, h string) bool { return h == "scout" || h == "reviewer" || h == "implementer" },
		ProcessKnown: func(_, h string) bool { return strings.HasPrefix(h, "cmd-") },
		ProjectChat:  func(_ context.Context, projectID string) bool { return projectID == testdbseed.DefaultProjectID },
	}))
	return reg, rootDir
}

func callWW04Tool(t *testing.T, reg *tools.DefaultRegistry, name string, args map[string]any, tctx tools.ToolContext) (map[string]any, *toolrejection.ToolReject) {
	t.Helper()
	def, ok := reg.Definition(name)
	if !ok {
		t.Fatalf("missing tool %s", name)
	}
	res, err := def.Handler(t.Context(), args, tctx)
	if err != nil {
		var rej *toolrejection.ToolReject
		if errors.As(err, &rej) {
			return nil, rej
		}
		t.Fatalf("unexpected non-reject error from %s: %v", name, err)
	}
	var decoded map[string]any
	testutil.FailErr(t, "decode tool json", json.Unmarshal([]byte(res), &decoded))
	return decoded, nil
}

// TestWW04_C07_SurfacesCoordinatorAndWorker covers ledger slots C07-surfaces-coordinator and C07-surfaces-worker:
// offered tool surfaces, actionable watch promotion/pruning, role & capacity validation, verbatim watch_match
// round-trip, worker wake rejection (WATCH_WAKE_NOT_ALLOWED), and web_research profile restrictions.
func TestWW04_C07_SurfacesCoordinatorAndWorker(t *testing.T) {
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	store, _, owner := openWW04ManualStore(t, base)
	reg, rootDir := newWW04ToolRegistry(t, store)

	// 1. Coordinator surface catalog matrix: verify watch/watch_control placement rules across surfaces.
	cat, err := surfacecatalog.Load()
	testutil.FailErr(t, "surfacecatalog.Load", err)
	if len(cat.SurfaceIDs()) == 0 {
		t.Fatal("expected non-empty coordinator surface catalog")
	}
	managedCount := 0
	floorWatchCount := 0
	for _, surfaceID := range cat.SurfaceIDs() {
		planWithRoot, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: surfaceID}, 1)
		testutil.FailErr(t, "CompileToolPlan "+surfaceID, err)
		if planWithRoot.Immediate("watch") {
			floorWatchCount++
		}
		if surface.AllowsWatchManagement(surfaceID) {
			managedCount++
			promoted := planWithRoot.Promote("watch_control")
			if !promoted.Immediate("watch_control") {
				t.Fatalf("surface %s allows watch management but Promote(watch_control) did not make it immediate", surfaceID)
			}
		}
	}
	if managedCount == 0 || floorWatchCount == 0 {
		t.Fatalf("expected non-zero managed (%d) and floor watch (%d) coordinator surfaces", managedCount, floorWatchCount)
	}

	coordCtx := func(callID string) tools.ToolContext {
		return tools.ToolContext{
			Identity: tools.InvocationIdentity{
				ProjectID:  owner.ProjectID,
				SessionID:  owner.SessionID,
				ToolCallID: callID,
				Agent:      "coordinator",
			},
			Source: tools.InvocationSource{
				ActiveRootID: "repo",
				Roots:        []projectroot.RootRef{{ID: "repo", Label: "repo", Path: rootDir, IsPrimary: true}},
			},
			Local: tools.InvocationLocal{
				LoopbackConnectGranted: true,
				LoopbackConnectPorts:   []uint16{8080},
			},
		}
	}

	// 2. Idle -> live -> terminal-pending -> drained actionable watch presence transitions.
	actionable, err := HasActionableWatches(t.Context(), store, owner)
	testutil.FailErr(t, "HasActionableWatches idle", err)
	if actionable {
		t.Fatal("expected HasActionableWatches=false when no watches exist")
	}

	receipt, rej := callWW04Tool(t, reg, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "process_done", "handles": []any{"cmd-101"}}},
	}, coordCtx("c07-reg-1"))
	if rej != nil {
		t.Fatalf("coordinator watch registration rejected: %+v", rej)
	}
	handle := receipt["handle"].(string)
	actionable, err = HasActionableWatches(t.Context(), store, owner)
	testutil.FailErr(t, "HasActionableWatches live", err)
	if !actionable {
		t.Fatal("expected HasActionableWatches=true when live watch exists")
	}

	// 3. Verbatim watch_match round-trip from watch receipt into SelectOrPark.
	wcMap, ok := receipt["wait_condition"].(map[string]any)
	if !ok || wcMap["kind"] != ConditionWatchMatch {
		t.Fatalf("unexpected wait_condition: %+v", receipt["wait_condition"])
	}
	rawWC, err := json.Marshal(wcMap)
	testutil.FailErr(t, "marshal wait_condition", err)
	var verbatimCond awaitstore.Condition
	testutil.FailErr(t, "unmarshal verbatim wait_condition", json.Unmarshal(rawWC, &verbatimCond))
	if verbatimCond.Kind != ConditionWatchMatch || len(verbatimCond.Handles) != 1 || verbatimCond.Handles[0] != handle {
		t.Fatalf("verbatim wait_condition mismatch: %+v", verbatimCond)
	}

	// Record terminal match so watch transitions to terminal with pending evidence.
	_, inserted, err := store.RecordMatch(t.Context(), receipt["watch_id"].(string), MatchInput{
		SourceKind:   ConditionProcessDone,
		OccurrenceID: "occ-c07-101",
		Outcome:      "satisfied",
		Evidence:     map[string]any{"handle": "cmd-101", "exit_code": 0},
	}, nil)
	testutil.FailErr(t, "record c07 match", err)
	if !inserted {
		t.Fatal("expected match insertion")
	}
	actionable, err = HasActionableWatches(t.Context(), store, owner)
	testutil.FailErr(t, "HasActionableWatches terminal pending", err)
	if !actionable {
		t.Fatal("expected HasActionableWatches=true when terminal watch still has pending evidence")
	}

	// Submit verbatim watch_match handles to SelectOrPark; must deliver immediately and drain pending evidence.
	sel, err := store.SelectOrPark(t.Context(), SelectRequest{
		Owner:     owner,
		Handles:   verbatimCond.Handles,
		AllowPark: false,
	})
	testutil.FailErr(t, "SelectOrPark verbatim watch_match", err)
	if sel.Parked || len(sel.Matches) != 1 || sel.Matches[0].Handle != handle {
		t.Fatalf("verbatim watch_match selection failed: %+v", sel)
	}
	actionable, err = HasActionableWatches(t.Context(), store, owner)
	testutil.FailErr(t, "HasActionableWatches drained", err)
	if actionable {
		t.Fatal("expected HasActionableWatches=false after terminal pending evidence drained")
	}

	// 4. Worker profile validation: wake rejected with WATCH_WAKE_NOT_ALLOWED on watch & watch_control,
	// coordinator-only conditions rejected with WAIT_CONDITION_NOT_ALLOWED, collect allowed with destination="job".
	_, err = store.Database().ExecContext(t.Context(), `INSERT INTO worker_jobs
		(id, project_id, parent_session_id, child_session_id, agent_type, prompt, brief, status, created_at)
		VALUES ('worker-job-c07', ?, ?, ?, 'implementer', 'work', 'work', 'running', '2026-11-27T12:00:00Z')`,
		owner.ProjectID, owner.SessionID, owner.SessionID)
	testutil.FailErr(t, "insert worker-job-c07", err)
	workerCtx := coordCtx("c07-worker-wake")
	workerCtx.Identity.Agent = "implement"
	workerCtx.Identity.WorkerJobID = "worker-job-c07"

	_, rej = callWW04Tool(t, reg, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "process_done", "handles": []any{"cmd-201"}}},
		"delivery":   "wake",
	}, workerCtx)
	if rej == nil || rej.Code != "WATCH_WAKE_NOT_ALLOWED" {
		t.Fatalf("worker wake registration got %+v, want WATCH_WAKE_NOT_ALLOWED", rej)
	}

	workerCtx.Identity.ToolCallID = "c07-worker-coord-cond"
	_, rej = callWW04Tool(t, reg, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "next_worker_done"}},
	}, workerCtx)
	if rej == nil || rej.Code != "WAIT_CONDITION_NOT_ALLOWED" {
		t.Fatalf("worker coordinator condition got %+v, want WAIT_CONDITION_NOT_ALLOWED", rej)
	}

	workerCtx.Identity.ToolCallID = "c07-worker-collect"
	wReceipt, rej := callWW04Tool(t, reg, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "process_done", "handles": []any{"cmd-202"}}},
		"delivery":   "collect",
	}, workerCtx)
	if rej != nil {
		t.Fatalf("worker collect watch rejected: %+v", rej)
	}
	if wReceipt["destination"] != "job" || wReceipt["delivery"] != "collect" {
		t.Fatalf("worker receipt = %+v, want destination=job delivery=collect", wReceipt)
	}

	workerCtx.Identity.ToolCallID = "c07-worker-set-wake"
	_, rej = callWW04Tool(t, reg, "watch_control", map[string]any{
		"handle":    wReceipt["handle"],
		"operation": "set_delivery",
		"delivery":  "wake",
	}, workerCtx)
	if rej == nil || rej.Code != "WATCH_WAKE_NOT_ALLOWED" {
		t.Fatalf("worker set_delivery=wake got %+v, want WATCH_WAKE_NOT_ALLOWED", rej)
	}

	// 5. web_research profile: cannot watch workspace/process/worker conditions.
	webCtx := coordCtx("c07-web-research")
	webCtx.Identity.Agent = "web_research"
	webCtx.Identity.WorkerJobID = "web-job-c07"
	for _, disallowed := range []map[string]any{
		{"kind": "process_done", "handles": []any{"cmd-301"}},
		{"kind": "file_change", "root": "repo", "path": "README.md"},
		{"kind": "git_ref", "root": "repo", "ref": "refs/heads/main"},
	} {
		_, rej = callWW04Tool(t, reg, "watch", map[string]any{"conditions": []any{disallowed}}, webCtx)
		if rej == nil || rej.Code != "WAIT_CONDITION_NOT_ALLOWED" {
			t.Fatalf("web_research condition %+v got %+v, want WAIT_CONDITION_NOT_ALLOWED", disallowed, rej)
		}
	}

	// 6. Capacity validation: 8 live explicit watches succeed, 9th rejected with WATCH_CAPACITY_REACHED and reclaimable handles.
	for i := range MaxLiveExplicitWatches {
		_, r := callWW04Tool(t, reg, "watch", map[string]any{
			"conditions": []any{map[string]any{"kind": "timer", "delay_ms": 60000}},
		}, coordCtx(fmt.Sprintf("c07-cap-%d", i)))
		if r != nil {
			t.Fatalf("capacity slot %d failed: %+v", i, r)
		}
	}
	_, rej = callWW04Tool(t, reg, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "timer", "delay_ms": 60000}},
	}, coordCtx("c07-cap-overflow"))
	if rej == nil || rej.Code != "WATCH_CAPACITY_REACHED" {
		t.Fatalf("9th live explicit watch got %+v, want WATCH_CAPACITY_REACHED", rej)
	}
	reclaimable, _ := rej.Data["watch_reclaimable_handles"].([]string)
	if len(reclaimable) != MaxLiveExplicitWatches {
		t.Fatalf("reclaimable handles len = %d, want %d", len(reclaimable), MaxLiveExplicitWatches)
	}
}

// TestWW04_C08_RaceCollectWaitWake covers ledger slot C08-race-collect-wait-wake:
// immediate & later matches, 3-way race of normal-turn collection, explicit wait, and idle wake
// for one occurrence -> exactly one presentation per destination owed; inspect does not re-consume.
func TestWW04_C08_RaceCollectWaitWake(t *testing.T) {
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	store, _, owner := openWW04ManualStore(t, base)

	// 1. Immediate match at registration when process is already finished.
	store.SetProcessLookup(func(_ context.Context, sessionID, handle string) (ProcessCompletionFact, bool, error) {
		if sessionID == owner.SessionID && handle == "cmd-already-done" {
			return ProcessCompletionFact{
				OccurrenceID: "occ-immediate-1",
				Handle:       "cmd-already-done",
				Outcome:      "satisfied",
				Report:       "exit_code=0",
			}, true, nil
		}
		return ProcessCompletionFact{}, false, nil
	})
	imm, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Delivery:   DeliveryCollect,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-already-done"}}},
	})
	testutil.FailErr(t, "register immediate watch", err)
	if imm.State != "matched" || imm.PendingMatches != 1 {
		t.Fatalf("expected immediate matched receipt, got %+v", imm)
	}
	// Inspect must NOT consume the pending match.
	inspBefore, err := store.Inspect(t.Context(), owner, imm.Handle)
	testutil.FailErr(t, "inspect immediate before drain", err)
	if inspBefore.PendingMatches != 1 {
		t.Fatalf("inspect consumed immediate match: %+v", inspBefore)
	}
	batchImm, okImm, err := ReserveCollectBatch(t.Context(), store, owner, "req:imm:1")
	testutil.FailErr(t, "reserve immediate batch", err)
	if !okImm || len(batchImm.Matches) != 1 || batchImm.Matches[0].OccurrenceID != "occ-immediate-1" {
		t.Fatalf("expected immediate match in collect batch, got ok=%v batch=%+v", okImm, batchImm)
	}
	testutil.FailErr(t, "commit immediate batch", CommitCollectBatch(t.Context(), store, owner.SessionID, batchImm.DeliveryID))
	inspAfter, err := store.Inspect(t.Context(), owner, imm.Handle)
	testutil.FailErr(t, "inspect immediate after commit", err)
	if inspAfter.PendingMatches != 0 || inspAfter.LastDeliveryRef != batchImm.DeliveryID {
		t.Fatalf("inspect after commit = %+v, want PendingMatches=0 LastDeliveryRef=%s", inspAfter, batchImm.DeliveryID)
	}

	// 2. 3-way race of normal-turn collection, explicit wait, and idle ambient wake for ONE occurrence.
	for trial := range 6 {
		trialStore, _, trialOwner := openWW04ManualStore(t, base)
		var wakeCount atomic.Int32
		var wakeDeliveryID atomic.Value
		trialStore.SetAmbientWakeGate(AmbientWakeGate{
			SessionEligible: func(_ context.Context, _ string) (bool, bool) { return true, false },
			WorkflowAdmit:   func(_ context.Context, _ string) (bool, bool) { return true, false },
			DispatchTurn: func(_ context.Context, _, submissionID, _ string, onAdmitted func() error) error {
				if err := onAdmitted(); err != nil {
					return err
				}
				wakeDeliveryID.Store(submissionID)
				wakeCount.Add(1)
				return nil
			},
		})

		// Register with DeliveryCollect first so RecordMatch commits the pending match without prematurely firing wake,
		// then flip to DeliveryWake (while session is momentarily ineligible) so all 3 routes race on the same pending match.
		var allowWake atomic.Bool
		trialStore.SetAmbientWakeGate(AmbientWakeGate{
			SessionEligible: func(_ context.Context, _ string) (bool, bool) { return allowWake.Load(), true },
			WorkflowAdmit:   func(_ context.Context, _ string) (bool, bool) { return true, false },
			DispatchTurn: func(_ context.Context, _, submissionID, _ string, onAdmitted func() error) error {
				if err := onAdmitted(); err != nil {
					return err
				}
				wakeDeliveryID.Store(submissionID)
				wakeCount.Add(1)
				return nil
			},
		})

		rec, err := trialStore.Register(t.Context(), RegisterRequest{
			Owner:      trialOwner,
			Delivery:   DeliveryWake,
			Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-race"}}},
		})
		testutil.FailErr(t, "register race watch", err)

		occID := fmt.Sprintf("occ-3way-trial-%d", trial)
		_, inserted, err := trialStore.RecordMatch(t.Context(), rec.WatchID, MatchInput{
			SourceKind:   ConditionProcessDone,
			OccurrenceID: occID,
			Outcome:      "satisfied",
			Evidence:     map[string]any{"handle": "cmd-race", "exit_code": 0},
		}, nil)
		testutil.FailErr(t, "record race match", err)
		if !inserted {
			t.Fatalf("trial %d: match not inserted", trial)
		}

		// Inspect before race must leave pending match intact.
		preView, err := trialStore.Inspect(t.Context(), trialOwner, rec.Handle)
		testutil.FailErr(t, "inspect pre-race", err)
		if preView.PendingMatches != 1 {
			t.Fatalf("trial %d: inspect pre-race consumed match: %+v", trial, preView)
		}

		// Launch all 3 competing routes concurrently behind a barrier.
		startGate := newWW02Barrier()
		allowWake.Store(true)

		var collectCount atomic.Int32
		var waitCount atomic.Int32
		var wg sync.WaitGroup
		wg.Add(3)

		go func() {
			defer wg.Done()
			startGate.WaitReleased(2 * time.Second)
			batch, ok, rErr := ReserveCollectBatch(t.Context(), trialStore, trialOwner, fmt.Sprintf("req:race:%d", trial))
			if rErr == nil && ok && len(batch.Matches) > 0 {
				if cErr := CommitCollectBatch(t.Context(), trialStore, trialOwner.SessionID, batch.DeliveryID); cErr == nil {
					collectCount.Add(1)
				}
			}
		}()

		go func() {
			defer wg.Done()
			startGate.WaitReleased(2 * time.Second)
			selRes, sErr := trialStore.SelectOrPark(t.Context(), SelectRequest{
				Owner:     trialOwner,
				Handles:   []string{rec.Handle},
				AllowPark: false,
			})
			if sErr == nil && !selRes.Parked && len(selRes.Matches) > 0 {
				waitCount.Add(1)
			}
		}()

		go func() {
			defer wg.Done()
			startGate.WaitReleased(2 * time.Second)
			_, _ = trialStore.TriggerAmbientWake(t.Context(), trialOwner.SessionID)
		}()

		startGate.Release()
		wg.Wait()

		total := collectCount.Load() + waitCount.Load() + wakeCount.Load()
		if total != 1 {
			t.Fatalf("trial %d: 3-way race produced %d presentations (collect=%d wait=%d wake=%d), want exactly 1",
				trial, total, collectCount.Load(), waitCount.Load(), wakeCount.Load())
		}

		// Inspect after race does not re-consume and preserves receipt reference.
		postView, err := trialStore.Inspect(t.Context(), trialOwner, rec.Handle)
		testutil.FailErr(t, "inspect post-race", err)
		if postView.PendingMatches != 0 || postView.LastDeliveryRef == "" {
			t.Fatalf("trial %d: post-race inspect = %+v, want PendingMatches=0 and non-empty LastDeliveryRef", trial, postView)
		}
	}
}

// TestWW04_C09_RepeatAndAlternatives covers ledger slot C09-repeat-alternatives:
// state condition false/true transitions, discrete event duplicate vs distinct IDs, rearm,
// coalescing, terminal-only repeat rejection, and recursive watch_match on watch rejection.
func TestWW04_C09_RepeatAndAlternatives(t *testing.T) {
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	store, _, owner := openWW04ManualStore(t, base)

	// 1. State condition (all_workers_idle) through true -> true (suppressed) -> false (rearm) -> true (ep2).
	stateRec, err := store.Register(t.Context(), RegisterRequest{
		Owner:    owner,
		Repeat:   true,
		Delivery: DeliveryCollect,
		Conditions: []awaitstore.Condition{
			{Kind: "all_workers_idle"},
			{Kind: "overlay_promote_pending"},
		},
	})
	testutil.FailErr(t, "register repeating state watch", err)

	// First transition to true -> fires ep1.
	matched, err := store.RecordWorkerEvent(t.Context(), owner.SessionID, WorkerWatchEvent{
		Kind:           "state",
		AllWorkersIdle: true,
	})
	testutil.FailErr(t, "state true 1", err)
	if !matched {
		t.Fatal("expected first all_workers_idle=true to match")
	}

	// Staying true without a false transition -> must NOT refire.
	matched, err = store.RecordWorkerEvent(t.Context(), owner.SessionID, WorkerWatchEvent{
		Kind:           "state",
		AllWorkersIdle: true,
	})
	testutil.FailErr(t, "state true duplicate", err)
	if matched {
		t.Fatal("repeated all_workers_idle=true without false transition must not refire")
	}

	// Transition to false -> rearms without matching.
	matched, err = store.RecordWorkerEvent(t.Context(), owner.SessionID, WorkerWatchEvent{
		Kind:           "state",
		AllWorkersIdle: false,
	})
	testutil.FailErr(t, "state false rearm", err)
	if matched {
		t.Fatal("all_workers_idle=false should rearm, not match")
	}

	// Transition back to true -> fires ep2.
	matched, err = store.RecordWorkerEvent(t.Context(), owner.SessionID, WorkerWatchEvent{
		Kind:           "state",
		AllWorkersIdle: true,
	})
	testutil.FailErr(t, "state true 2", err)
	if !matched {
		t.Fatal("expected rearmed all_workers_idle=true to fire ep2")
	}

	// Alternative condition (overlay_promote_pending) also fires on the same repeating watch.
	matched, err = store.RecordWorkerEvent(t.Context(), owner.SessionID, WorkerWatchEvent{
		Kind:                  "state",
		AllWorkersIdle:        true,
		OverlayPromotePending: true,
	})
	testutil.FailErr(t, "alternative overlay_promote_pending", err)
	if !matched {
		t.Fatal("expected alternative overlay_promote_pending to match")
	}

	// Coalescing: all 3 pending matches coalesce into one collected batch.
	batchState, ok, err := ReserveCollectBatch(t.Context(), store, owner, "req:c09:state")
	testutil.FailErr(t, "reserve coalesced state batch", err)
	if !ok || len(batchState.Matches) != 3 {
		t.Fatalf("expected 3 coalesced matches (ep1, ep2, overlay), got ok=%v len=%d", ok, len(batchState.Matches))
	}
	testutil.FailErr(t, "commit coalesced state batch", CommitCollectBatch(t.Context(), store, owner.SessionID, batchState.DeliveryID))

	// 2. Discrete event duplicate vs distinct OccurrenceIDs.
	discRec, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Repeat:     true,
		Delivery:   DeliveryCollect,
		Conditions: []awaitstore.Condition{{Kind: "next_worker_done"}},
	})
	testutil.FailErr(t, "register repeating discrete watch", err)

	_, ins1, err := store.RecordMatch(t.Context(), discRec.WatchID, MatchInput{
		SourceKind: "next_worker_done", OccurrenceID: "worker_terminal:job-1", Outcome: "satisfied",
		Evidence: map[string]any{"job_id": "job-1"},
	}, nil)
	testutil.FailErr(t, "discrete occ 1", err)
	_, insDup, err := store.RecordMatch(t.Context(), discRec.WatchID, MatchInput{
		SourceKind: "next_worker_done", OccurrenceID: "worker_terminal:job-1", Outcome: "satisfied",
		Evidence: map[string]any{"job_id": "job-1"},
	}, nil)
	testutil.FailErr(t, "discrete occ 1 dup", err)
	_, ins2, err := store.RecordMatch(t.Context(), discRec.WatchID, MatchInput{
		SourceKind: "next_worker_done", OccurrenceID: "worker_terminal:job-2", Outcome: "satisfied",
		Evidence: map[string]any{"job_id": "job-2"},
	}, nil)
	testutil.FailErr(t, "discrete occ 2", err)
	if !ins1 || insDup || !ins2 {
		t.Fatalf("discrete dedup failed: ins1=%v insDup=%v ins2=%v", ins1, insDup, ins2)
	}
	batchDisc, ok, err := ReserveCollectBatch(t.Context(), store, owner, "req:c09:disc")
	testutil.FailErr(t, "reserve discrete batch", err)
	if !ok || len(batchDisc.Matches) != 2 {
		t.Fatalf("expected 2 distinct coalesced discrete matches, got ok=%v len=%d", ok, len(batchDisc.Matches))
	}
	testutil.FailErr(t, "commit discrete batch", CommitCollectBatch(t.Context(), store, owner.SessionID, batchDisc.DeliveryID))

	// 3. Terminal-only repeat rejections: process_done with exact handles, scan_done, absolute timer.
	terminalCases := []struct {
		name string
		cond awaitstore.Condition
	}{
		{"process_done_exact", awaitstore.Condition{Kind: ConditionProcessDone, Handles: []string{"cmd-1"}}},
		{"scan_done", awaitstore.Condition{Kind: "scan_done"}},
		{"absolute_timer", awaitstore.Condition{Kind: "timer", At: base.Add(5 * time.Minute).Format(time.RFC3339)}},
	}
	for _, tc := range terminalCases {
		_, rErr := store.Register(t.Context(), RegisterRequest{
			Owner:      owner,
			Repeat:     true,
			Conditions: []awaitstore.Condition{tc.cond},
		})
		var valErr *ValidationError
		if !errors.As(rErr, &valErr) || valErr.Code != "WATCH_REPEAT_UNSUPPORTED" {
			t.Fatalf("terminal repeat case %s got %v, want WATCH_REPEAT_UNSUPPORTED", tc.name, rErr)
		}
	}

	// 4. Recursive watch_match on watch rejected with WATCH_CONDITION_INVALID.
	_, rErr := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: ConditionWatchMatch, Handles: []string{stateRec.Handle}}},
	})
	var valErr *ValidationError
	if !errors.As(rErr, &valErr) || valErr.Code != "WATCH_CONDITION_INVALID" {
		t.Fatalf("recursive watch_match got %v, want WATCH_CONDITION_INVALID", rErr)
	}
}

// TestWW04_C10_ExpiryCancelRace covers ledger slot C10-expiry-cancel-race:
// manual clock (ww02ManualClock) with 2 differently expiring watches + worker condition,
// wait timeout before expiry, expiry before deadline, cancel racing match, explicit set_delivery
// mode change, capacity reuse, and producer teardown.
func TestWW04_C10_ExpiryCancelRace(t *testing.T) {
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	store, clk, owner := openWW04ManualStore(t, base)

	var wokeMu sync.Mutex
	var wokeEvents []wokeLease
	store.SetLeaseWakeHandler(func(_ context.Context, lease awaitstore.Lease, winner awaitstore.Condition) {
		wokeMu.Lock()
		wokeEvents = append(wokeEvents, wokeLease{lease: lease, winner: winner})
		wokeMu.Unlock()
	})

	// Register 2 differently expiring watches (+2m and +10m) plus a worker condition.
	wShort, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Delivery:   DeliveryCollect,
		ExpiresAt:  base.Add(2 * time.Minute),
		Conditions: []awaitstore.Condition{{Kind: "next_worker_done", Handles: []string{"scout"}}},
	})
	testutil.FailErr(t, "register short-expiry watch", err)

	wLong, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Delivery:   DeliveryCollect,
		ExpiresAt:  base.Add(10 * time.Minute),
		Conditions: []awaitstore.Condition{{Kind: "worker_failed", Handles: []string{"reviewer"}}},
	})
	testutil.FailErr(t, "register long-expiry watch", err)

	// 1. Wait timeout BEFORE either watch expires (deadline at +1m < short expiry +2m).
	parkTimeout, err := store.SelectOrPark(t.Context(), SelectRequest{
		Owner:     owner,
		Handles:   []string{wShort.Handle, wLong.Handle},
		AllowPark: true,
		ParkLease: awaitstore.Lease{
			SessionID: owner.SessionID,
			ProjectID: owner.ProjectID,
			ToolCallID: "wait-timeout-before-expiry",
			Deadline:  base.Add(1 * time.Minute),
			Conditions: []awaitstore.Condition{
				{Kind: ConditionWatchMatch, Handles: []string{wShort.Handle, wLong.Handle}},
				{Kind: "next_worker_done"},
			},
		},
	})
	testutil.FailErr(t, "park wait timeout before expiry", err)
	if !parkTimeout.Parked {
		t.Fatalf("expected parked wait, got %+v", parkTimeout)
	}

	clk.Advance(1 * time.Minute)
	testutil.FailErr(t, "advance to wait timeout", store.AdvanceDue(t.Context()))
	wokeMu.Lock()
	if len(wokeEvents) != 1 || wokeEvents[0].winner.Outcome != "timed_out" {
		t.Fatalf("expected timed_out wake at +1m, got %+v", wokeEvents)
	}
	if !strings.Contains(wokeEvents[0].winner.Report, wShort.Handle) || !strings.Contains(wokeEvents[0].winner.Report, wLong.Handle) {
		t.Fatalf("expected both handles active on timeout, report=%s", wokeEvents[0].winner.Report)
	}
	wokeMu.Unlock()

	// 2. Expiry BEFORE wait deadline (park deadline at +5m, short watch expires at +2m).
	parkExpiry, err := store.SelectOrPark(t.Context(), SelectRequest{
		Owner:     owner,
		Handles:   []string{wShort.Handle, wLong.Handle},
		AllowPark: true,
		ParkLease: awaitstore.Lease{
			SessionID:  owner.SessionID,
			ProjectID:  owner.ProjectID,
			ToolCallID: "wait-expiry-before-deadline",
			Deadline:   base.Add(5 * time.Minute),
			Conditions: []awaitstore.Condition{
				{Kind: ConditionWatchMatch, Handles: []string{wShort.Handle, wLong.Handle}},
			},
		},
	})
	testutil.FailErr(t, "park wait expiry before deadline", err)
	if !parkExpiry.Parked {
		t.Fatalf("expected parked wait, got %+v", parkExpiry)
	}

	clk.Advance(1 * time.Minute) // now = +2m (exact wShort expiry)
	testutil.FailErr(t, "advance to short watch expiry", store.AdvanceDue(t.Context()))
	wokeMu.Lock()
	if len(wokeEvents) != 2 || wokeEvents[1].winner.Outcome != string(OutcomeExpired) {
		t.Fatalf("expected expired wake at +2m, got %+v", wokeEvents)
	}
	var report struct {
		ActiveHandles []string           `json:"active_handles"`
		Terminal      *TerminalSelection `json:"terminal"`
	}
	testutil.FailErr(t, "decode expiry report", json.Unmarshal([]byte(wokeEvents[1].winner.Report), &report))
	wokeMu.Unlock()
	if report.Terminal == nil || report.Terminal.Handle != wShort.Handle || report.Terminal.Outcome != OutcomeExpired {
		t.Fatalf("unexpected terminal selection on expiry: %+v", report.Terminal)
	}
	if len(report.ActiveHandles) != 1 || report.ActiveHandles[0] != wLong.Handle {
		t.Fatalf("expected wLong active after wShort expiry, got %+v", report.ActiveHandles)
	}

	// 3. Explicit set_delivery mode change on live watch, and WATCH_TERMINAL on expired watch.
	mode, err := store.SetDelivery(t.Context(), owner, wLong.Handle, DeliveryWake)
	testutil.FailErr(t, "set_delivery collect->wake", err)
	if mode != DeliveryWake {
		t.Fatalf("mode = %s, want wake", mode)
	}
	mode, err = store.SetDelivery(t.Context(), owner, wLong.Handle, DeliveryCollect)
	testutil.FailErr(t, "set_delivery wake->collect", err)
	if mode != DeliveryCollect {
		t.Fatalf("mode = %s, want collect", mode)
	}
	if _, err := store.SetDelivery(t.Context(), owner, wShort.Handle, DeliveryWake); err == nil {
		t.Fatal("expected set_delivery on expired watch to fail")
	} else {
		var termErr *TerminalError
		if !errors.As(err, &termErr) {
			t.Fatalf("expected TerminalError on expired watch, got %v", err)
		}
	}

	// 4. Cancel racing match: exactly one terminal outcome, idempotent cancel receipt, no post-cancel matches.
	wRace, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Delivery:   DeliveryCollect,
		ExpiresAt:  clk.Now().Add(10 * time.Minute),
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-cancel-race"}}},
	})
	testutil.FailErr(t, "register cancel-race watch", err)

	raceGate := newWW02Barrier()
	var cancelRcpt CancelReceipt
	var cancelErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		raceGate.WaitReleased(2 * time.Second)
		cancelRcpt, cancelErr = store.Cancel(t.Context(), owner, wRace.Handle)
	}()
	go func() {
		defer wg.Done()
		raceGate.WaitReleased(2 * time.Second)
		_, _, _ = store.RecordMatch(t.Context(), wRace.WatchID, MatchInput{
			SourceKind:   ConditionProcessDone,
			OccurrenceID: "occ-cancel-race",
			Outcome:      "satisfied",
			Evidence:     map[string]any{"handle": "cmd-cancel-race"},
		}, nil)
	}()
	raceGate.Release()
	wg.Wait()
	testutil.FailErr(t, "cancel in race", cancelErr)
	if cancelRcpt.State != StateTerminal || (cancelRcpt.Outcome != OutcomeCanceled && cancelRcpt.Outcome != OutcomeCompleted) {
		t.Fatalf("unexpected cancel-race outcome: %+v", cancelRcpt)
	}
	// Repeat cancel returns identical final receipt.
	cancelRcpt2, err := store.Cancel(t.Context(), owner, wRace.Handle)
	testutil.FailErr(t, "idempotent second cancel", err)
	if cancelRcpt2.ReceiptID != cancelRcpt.ReceiptID {
		t.Fatalf("second cancel receipt_id = %s, want %s", cancelRcpt2.ReceiptID, cancelRcpt.ReceiptID)
	}
	// Post-cancel match is rejected.
	_, postIns, err := store.RecordMatch(t.Context(), wRace.WatchID, MatchInput{
		SourceKind:   ConditionProcessDone,
		OccurrenceID: "occ-after-cancel",
		Outcome:      "satisfied",
	}, nil)
	testutil.FailErr(t, "post-cancel match attempt", err)
	if postIns {
		t.Fatal("post-cancel match must not be inserted")
	}

	// 5. Capacity reuse & producer teardown: fill remaining live slots to MaxLiveExplicitWatches, verify full,
	// cancel wLong -> verify immediate slot reuse & heap cleanup.
	// Currently only wLong is live (wShort expired, wRace canceled).
	for i := 1; i < MaxLiveExplicitWatches; i++ {
		_, err := store.Register(t.Context(), RegisterRequest{
			Owner:      owner,
			ExpiresAt:  clk.Now().Add(15 * time.Minute),
			Conditions: []awaitstore.Condition{{Kind: "timer", DelayMS: 60000}},
		})
		testutil.FailErr(t, fmt.Sprintf("fill slot %d", i), err)
	}
	if _, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		ExpiresAt:  clk.Now().Add(15 * time.Minute),
		Conditions: []awaitstore.Condition{{Kind: "timer", DelayMS: 60000}},
	}); err == nil {
		t.Fatal("expected CapacityError at 8 live watches")
	} else {
		var capErr *CapacityError
		if !errors.As(err, &capErr) {
			t.Fatalf("expected *CapacityError, got %v", err)
		}
	}

	// Cancel wLong -> frees slot immediately and tears down its expiry timer.
	_, err = store.Cancel(t.Context(), owner, wLong.Handle)
	testutil.FailErr(t, "cancel wLong to reuse slot", err)
	reused, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		ExpiresAt:  clk.Now().Add(15 * time.Minute),
		Conditions: []awaitstore.Condition{{Kind: "timer", DelayMS: 60000}},
	})
	testutil.FailErr(t, "reuse freed capacity slot", err)
	if reused.Handle == "" {
		t.Fatal("expected valid handle on reused capacity slot")
	}
}

// TestWW04_C11_C12_C13_RolesHoldsHandoffAndWorkerDecisions covers ledger slots:
// - C11-roles-and-holds: job replacement, session end, approval/budget/input holds with authorized turn count.
// - C12-bash-command-handoff: derived vs explicit vs direct process wait single delivery, shell join, H1-H3 replay orderings.
// - C13-worker-decision-failure: open/close worker decision preserved for owner, worker fail + retry new job identity.
func TestWW04_C11_C12_C13_RolesHoldsHandoffAndWorkerDecisions(t *testing.T) {
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

	// --- C11: Roles, job replacement, session end, and holds ---
	t.Run("C11_RolesAndHolds", func(t *testing.T) {
		store, _, owner := openWW04ManualStore(t, base)
		workerSessID := "worker-sess-c11"
		testdbseed.InsertChildSession(t, store.Database(), workerSessID, owner.SessionID)
		for _, jobID := range []string{"job-a", "job-b"} {
			_, err := store.Database().ExecContext(t.Context(), `INSERT INTO worker_jobs
				(id, project_id, parent_session_id, child_session_id, agent_type, prompt, brief, status, created_at)
				VALUES (?, ?, ?, ?, 'implementer', 'work', 'work', 'running', '2026-11-27T12:00:00Z')`,
				jobID, owner.ProjectID, owner.SessionID, workerSessID)
			testutil.FailErr(t, "insert worker job "+jobID, err)
		}

		jobA := Owner{Kind: OwnerJob, ProjectID: owner.ProjectID, SessionID: workerSessID, WorkerJobID: "job-a"}
		jobB := Owner{Kind: OwnerJob, ProjectID: owner.ProjectID, SessionID: workerSessID, WorkerJobID: "job-b"}

		wJobA, err := store.Register(t.Context(), RegisterRequest{
			Owner:      jobA,
			Delivery:   DeliveryCollect,
			Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-a"}}},
		})
		testutil.FailErr(t, "register job-a watch", err)
		wJobB, err := store.Register(t.Context(), RegisterRequest{
			Owner:      jobB,
			Delivery:   DeliveryCollect,
			Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-b"}}},
		})
		testutil.FailErr(t, "register job-b watch", err)

		// Replace job-a via RetireOwner: job-a watch becomes terminal/canceled, job-b stays live.
		testutil.FailErr(t, "retire job-a", store.RetireOwner(t.Context(), jobA))
		viewA, err := store.Inspect(t.Context(), jobA, wJobA.Handle)
		testutil.FailErr(t, "inspect job-a after retire", err)
		viewB, err := store.Inspect(t.Context(), jobB, wJobB.Handle)
		testutil.FailErr(t, "inspect job-b after job-a retire", err)
		if viewA.State != StateTerminal || viewA.Outcome != OutcomeCanceled {
			t.Fatalf("job-a watch not retired: %+v", viewA)
		}
		if viewB.State != StateLive {
			t.Fatalf("job-b watch affected by job-a retirement: %+v", viewB)
		}

		// Approval / budget / pending-input holds during ambient wake.
		var (
			pendingUserInput = true
			approvalHold     = true
			budgetHold       = true
			admittedCount    int
			admittedPayloads []string
		)
		store.SetAmbientWakeGate(AmbientWakeGate{
			SessionEligible: func(_ context.Context, _ string) (bool, bool) {
				return !pendingUserInput && !budgetHold, true
			},
			WorkflowAdmit: func(_ context.Context, _ string) (bool, bool) {
				return !approvalHold, true
			},
			DispatchTurn: func(_ context.Context, _, _, text string, onAdmitted func() error) error {
				if err := onAdmitted(); err != nil {
					return err
				}
				admittedCount++
				admittedPayloads = append(admittedPayloads, text)
				return nil
			},
		})

		wWake, err := store.Register(t.Context(), RegisterRequest{
			Owner:      owner,
			Delivery:   DeliveryWake,
			Repeat:     true,
			Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone}},
		})
		testutil.FailErr(t, "register coordinator wake watch", err)

		_, _, err = store.RecordMatch(t.Context(), wWake.WatchID, MatchInput{
			SourceKind:   ConditionProcessDone,
			OccurrenceID: "occ-hold-1",
			Outcome:      "satisfied",
			Evidence:     map[string]any{"tag": "held-evidence"},
		}, nil)
		testutil.FailErr(t, "record match under holds", err)
		if admittedCount != 0 {
			t.Fatalf("expected 0 turns while pendingUserInput/approval/budget held, got %d", admittedCount)
		}

		pendingUserInput = false
		woke, err := store.TriggerAmbientWake(t.Context(), owner.SessionID)
		testutil.FailErr(t, "trigger under budget+approval hold", err)
		if woke || admittedCount != 0 {
			t.Fatalf("expected hold to block wake, woke=%v admitted=%d", woke, admittedCount)
		}

		budgetHold = false
		woke, err = store.TriggerAmbientWake(t.Context(), owner.SessionID)
		testutil.FailErr(t, "trigger under approval hold", err)
		if woke || admittedCount != 0 {
			t.Fatalf("expected approval hold to block wake, woke=%v admitted=%d", woke, admittedCount)
		}

		// Release final hold -> exactly 1 authorized turn admitted carrying preserved evidence.
		approvalHold = false
		woke, err = store.TriggerAmbientWake(t.Context(), owner.SessionID)
		testutil.FailErr(t, "trigger after all holds released", err)
		if !woke || admittedCount != 1 || !strings.Contains(admittedPayloads[0], "held-evidence") {
			t.Fatalf("expected 1 admitted turn with held-evidence, woke=%v count=%d payloads=%v", woke, admittedCount, admittedPayloads)
		}

		// Session end retires remaining live coordinator watches.
		testutil.FailErr(t, "retire session owner", store.RetireOwner(t.Context(), owner))
		viewWake, err := store.Inspect(t.Context(), owner, wWake.Handle)
		testutil.FailErr(t, "inspect wake watch after session end", err)
		if viewWake.State != StateTerminal || viewWake.Outcome != OutcomeCanceled {
			t.Fatalf("session watch not retired on session end: %+v", viewWake)
		}
	})

	// --- C12: Command handoff, derived vs explicit vs direct race, shell join, H1-H3 ---
	t.Run("C12_BashCommandHandoff", func(t *testing.T) {
		store, clk, owner := openWW04ManualStore(t, base)
		auth := Authority{RootSessionID: owner.SessionID}

		// Derived watch + explicit watch on same command handle + direct wait presentation claim.
		derived, err := store.HandoffDerivedCommand(t.Context(), owner, auth, "cmd-bg-1", 42)
		testutil.FailErr(t, "handoff derived command", err)
		if derived.WatchHandle == "" || derived.WaitCondition.Kind != ConditionWatchMatch || derived.OutputCursor != 42 {
			t.Fatalf("unexpected derived handoff: %+v", derived)
		}
		explicit, err := store.Register(t.Context(), RegisterRequest{
			Owner:      owner,
			Delivery:   DeliveryCollect,
			Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-bg-1"}}},
		})
		testutil.FailErr(t, "register explicit watch on same command", err)

		// Direct process wait claims and admits the occurrence first.
		dec, err := ClaimPresentation(t.Context(), store, owner, "process_done:cmd-bg-1", ClaimDirectWait, true, "direct-receipt-1")
		testutil.FailErr(t, "direct wait claim", err)
		if !dec.Granted {
			t.Fatalf("expected direct wait claim granted, got %+v", dec)
		}

		// Process completion arrives: both derived and explicit watches reconcile against the already-admitted claim
		// so zero duplicate pending matches are queued for model delivery.
		outcome, err := store.RecordProcessCompletion(t.Context(), owner.SessionID, ProcessCompletionFact{
			OccurrenceID: "process_done:cmd-bg-1",
			Handle:       "cmd-bg-1",
			Outcome:      "satisfied",
			Report:       "exit_code=0",
		})
		testutil.FailErr(t, "record process completion after direct admit", err)
		if !outcome.Claimed || len(outcome.Matches) != 0 {
			t.Fatalf("expected already-admitted reconciliation with 0 new pending matches, got %+v", outcome)
		}
		if _, hasDup, err := ReserveCollectBatch(t.Context(), store, owner, "req:c12:nodup"); err != nil || hasDup {
			t.Fatalf("duplicate model delivery after direct wait admit: hasDup=%v err=%v", hasDup, err)
		}
		_ = explicit

		// Shell join reconciliation: ReconcileShellJoin settles derived watch without model delivery.
		joinedHandoff, err := store.HandoffDerivedCommand(t.Context(), owner, auth, "cmd-joined-2", 0)
		testutil.FailErr(t, "handoff joined command", err)
		testutil.FailErr(t, "reconcile shell join", store.ReconcileShellJoin(t.Context(), owner, "cmd-joined-2"))
		joinedView, err := store.Inspect(t.Context(), owner, joinedHandoff.WatchHandle)
		testutil.FailErr(t, "inspect joined derived watch", err)
		if joinedView.State != StateTerminal || joinedView.Outcome != OutcomeCompleted || joinedView.PendingMatches != 0 {
			t.Fatalf("joined derived watch not settled cleanly: %+v", joinedView)
		}

		// Historical replay orderings H1 (parked before exit), H2 (exit before select), H3 (quiet collection).
		var h1Wakes atomic.Int32
		store.SetLeaseWakeHandler(func(_ context.Context, _ awaitstore.Lease, _ awaitstore.Condition) {
			h1Wakes.Add(1)
		})
		// H1: park on derived watch, then exit.
		h1, err := store.HandoffDerivedCommand(t.Context(), owner, auth, "cmd-h1", 0)
		testutil.FailErr(t, "handoff h1", err)
		parkedH1, err := store.SelectOrPark(t.Context(), SelectRequest{
			Owner:     owner,
			Handles:   []string{h1.WatchHandle},
			AllowPark: true,
			ParkLease: awaitstore.Lease{SessionID: owner.SessionID, ProjectID: owner.ProjectID, Deadline: clk.Now().Add(5 * time.Minute)},
		})
		testutil.FailErr(t, "park h1", err)
		if !parkedH1.Parked {
			t.Fatal("H1 expected parked lease")
		}
		_, err = store.RecordProcessCompletion(t.Context(), owner.SessionID, ProcessCompletionFact{
			OccurrenceID: "h1:exit:0", Handle: "cmd-h1", Outcome: "satisfied", Report: "exit_code=0",
		})
		testutil.FailErr(t, "complete h1", err)
		if h1Wakes.Load() != 1 {
			t.Fatalf("H1 expected 1 lease wake, got %d", h1Wakes.Load())
		}

		// H2: exit before select -> immediate in-turn delivery, no duplicate collect.
		h2, err := store.HandoffDerivedCommand(t.Context(), owner, auth, "cmd-h2", 0)
		testutil.FailErr(t, "handoff h2", err)
		_, err = store.RecordProcessCompletion(t.Context(), owner.SessionID, ProcessCompletionFact{
			OccurrenceID: "h2:exit:128", Handle: "cmd-h2", Outcome: "satisfied", Report: "exit_code=128",
		})
		testutil.FailErr(t, "complete h2", err)
		selH2, err := store.SelectOrPark(t.Context(), SelectRequest{
			Owner:     owner,
			Handles:   []string{h2.WatchHandle},
			AllowPark: true,
		})
		testutil.FailErr(t, "select h2", err)
		if selH2.Parked || len(selH2.Matches) != 1 || selH2.Matches[0].OccurrenceID != "h2:exit:128" {
			t.Fatalf("H2 expected immediate single match, got %+v", selH2)
		}

		// H3: quiet derived collection without explicit wait.
		h3, err := store.HandoffDerivedCommand(t.Context(), owner, auth, "cmd-h3", 0)
		testutil.FailErr(t, "handoff h3", err)
		_, err = store.RecordProcessCompletion(t.Context(), owner.SessionID, ProcessCompletionFact{
			OccurrenceID: "h3:exit:0", Handle: "cmd-h3", Outcome: "satisfied", Report: "exit_code=0",
		})
		testutil.FailErr(t, "complete h3", err)
		bH3, okH3, err := ReserveCollectBatch(t.Context(), store, owner, "req:h3")
		testutil.FailErr(t, "collect h3", err)
		if !okH3 || len(bH3.Matches) != 1 || bH3.Matches[0].OccurrenceID != "h3:exit:0" {
			t.Fatalf("H3 expected quiet collected match, got ok=%v batch=%+v", okH3, bH3)
		}
		testutil.FailErr(t, "commit h3", CommitCollectBatch(t.Context(), store, owner.SessionID, bH3.DeliveryID))
		if watched := store.WatchedCommandHandles(t.Context(), owner.SessionID); watched["cmd-h3"] != "" {
			t.Fatalf("H3 drained handle still reported as watched: %+v", watched)
		}
		_ = h3
	})

	// --- C13: Worker-specific observations (decision open/close, worker fail, retry new job identity) ---
	t.Run("C13_WorkerDecisionAndFailureRetry", func(t *testing.T) {
		store, _, owner := openWW04ManualStore(t, base)

		decWatch, err := store.Register(t.Context(), RegisterRequest{
			Owner:      owner,
			Repeat:     true,
			Delivery:   DeliveryCollect,
			Conditions: []awaitstore.Condition{{Kind: "worker_decision_needed", Handles: []string{"scout"}}},
		})
		testutil.FailErr(t, "register decision watch", err)
		failWatch, err := store.Register(t.Context(), RegisterRequest{
			Owner:      owner,
			Repeat:     true,
			Delivery:   DeliveryCollect,
			Conditions: []awaitstore.Condition{{Kind: "worker_failed", Handles: []string{"scout"}}},
		})
		testutil.FailErr(t, "register worker_failed watch", err)

		// Open worker decision: observation records match and preserves open decision status for owner.
		req := api.WorkerDecisionRequest{
			ChildSessionID: "worker-child-c13",
			WorkerID:       "job-c13-1",
			Question:       "Proceed with schema migration?",
			Options:        []string{"yes", "no"},
		}
		claimed, err := store.RecordWorkerDecisionOpened(t.Context(), owner.SessionID, "scout", "job-c13-1", req)
		testutil.FailErr(t, "record worker decision opened", err)
		if !claimed {
			t.Fatal("expected worker_decision_needed match")
		}
		openView, err := store.Inspect(t.Context(), owner, decWatch.Handle)
		testutil.FailErr(t, "inspect open decision", err)
		if openView.PendingMatches != 1 || openView.DecisionStatus["scout"] != "open" {
			t.Fatalf("open decision inspect = %+v, want pending=1 status=open", openView)
		}

		// Close worker decision: Inspect reflects closed status without losing pending match evidence.
		testutil.FailErr(t, "close worker decision", store.RecordWorkerDecisionClosed(t.Context(), owner.SessionID, "scout", "job-c13-1", "closed"))
		closedView, err := store.Inspect(t.Context(), owner, decWatch.Handle)
		testutil.FailErr(t, "inspect closed decision", err)
		if closedView.DecisionStatus["scout"] != "closed" {
			t.Fatalf("closed decision inspect = %+v, want status=closed", closedView)
		}

		// Worker job-c13-1 fails -> worker_failed watch matches.
		failed1, err := store.RecordWorkerEvent(t.Context(), owner.SessionID, WorkerWatchEvent{
			Kind:         "terminal",
			WorkerHandle: "scout",
			JobID:        "job-c13-1",
			Outcome:      "failed",
			Status:       "failed",
			Summary:      "first attempt failed",
		})
		testutil.FailErr(t, "record worker failure job-1", err)
		if !failed1 {
			t.Fatal("expected worker_failed match for job-c13-1")
		}

		// Retry under new job identity job-c13-2 -> distinct occurrence recorded independently.
		failed2, err := store.RecordWorkerEvent(t.Context(), owner.SessionID, WorkerWatchEvent{
			Kind:         "terminal",
			WorkerHandle: "scout",
			JobID:        "job-c13-2",
			Outcome:      "failed",
			Status:       "failed",
			Summary:      "retry attempt failed under new job identity",
		})
		testutil.FailErr(t, "record worker failure job-2", err)
		if !failed2 {
			t.Fatal("expected distinct worker_failed match for retry job-c13-2")
		}

		batch, ok, err := ReserveCollectBatch(t.Context(), store, owner, "req:c13:all")
		testutil.FailErr(t, "collect c13 batch", err)
		if !ok || len(batch.Matches) != 3 {
			t.Fatalf("expected 3 matches (decision + job-1 fail + job-2 fail), got ok=%v len=%d", ok, len(batch.Matches))
		}
		testutil.FailErr(t, "commit c13 batch", CommitCollectBatch(t.Context(), store, owner.SessionID, batch.DeliveryID))
		_ = failWatch

		// Emit schema_version=1 in-process evidence bundle.
		outPath := filepath.Join(t.TempDir(), "ww04-inprocess-evidence.json")
		writeWW02Evidence(t, outPath, ww02EvidenceBundle{
			SchemaVersion: 1,
			RunID:         "ww04-inprocess-c07-c13",
			Lane:          "in-process",
			ClockDomain:   "manual",
			CaseID:        "WW04-agent-watches",
			Expectations: map[string]any{
				"expected_outcome": "delivered",
				"owed_recipients":  []string{"session:session-1"},
				"owed_occurrences": []string{"worker_failed:job-c13-1", "worker_failed:job-c13-2"},
			},
			Observers: map[string]any{
				"source":         map[string]any{"observer": "source", "clock_domain": "manual"},
				"storage":        map[string]any{"observer": "storage", "clock_domain": "manual", "available": true, "armed_lease_count": 0},
				"admission":      map[string]any{"observer": "admission", "clock_domain": "manual"},
				"rendered_input": map[string]any{"observer": "rendered_input", "clock_domain": "manual", "presentations": []any{}},
				"den":            map[string]any{"observer": "den", "status": "not_run"},
			},
		})
		if info, err := os.Stat(outPath); err != nil || info.Size() == 0 {
			t.Fatalf("expected non-empty WW04 evidence bundle at %s: err=%v", outPath, err)
		}
	})
}
