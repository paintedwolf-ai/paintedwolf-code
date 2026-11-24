package watch_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/watch"
)

func TestWaitWithWatchMatchModeTimeoutsUntilCompleteAndResume(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	clock := testutil.NewFakeClock(time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC))
	watchStore := watch.NewStore(database, clock)
	waitStore := &awaitstore.Store{DB: database}

	loop := loopwake.NewLoopEngine()
	t.Cleanup(func() { loop.ForgetSession(context.Background(), "session-1") })
	loop.SetDeps(loopwake.LoopDeps{
		LoopProcessDeps: loopwake.LoopProcessDeps{
			ProcessState: func(sessionID, handle string) (bool, bool) {
				return sessionID == "session-1" && handle == "cmd-known", true
			},
		},
	})
	reg := tools.NewDefaultRegistry()
	err := loopwake.RegisterWaitTool(reg, loop.Subscriptions, loopwake.WaitToolDeps{
		Store:   waitStore,
		Watches: watchStore,
		ProfileConditions: map[string]map[string]bool{
			"implement": {"process_done": true, "watch_match": true},
		},
		RuntimeContext: t.Context(),
		WorkerKnown:    func(context.Context, string, string) bool { return true },
	})
	testutil.FailErr(t, "register wait tool", err)

	owner := watch.Owner{ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1"}
	w1, err := watchStore.Register(t.Context(), watch.RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: "process_done", Handles: []string{"cmd-known"}}},
	})
	testutil.FailErr(t, "register watch", err)

	invoker, ok := reg.Definition("wait")
	if !ok {
		t.Fatal("wait tool missing")
	}
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{
			SessionID: "session-1", ProjectID: testdbseed.DefaultProjectID,
			ToolCallID: "call-default", Agent: "implement",
		},
		Effects: tools.InvocationEffects{Out: &tools.ToolInvocationOut{}},
	}

	// Omitted timeout_ms resolves to 60s default even when selecting a 7-day watch.
	rawDefault, err := invoker.Handler(t.Context(), map[string]any{
		"conditions": []any{map[string]any{"kind": "watch_match", "handles": []any{w1.Handle}}},
	}, tctx)
	testutil.FailErr(t, "invoke wait with omitted timeout", err)
	var resDefault loopwake.WaitToolResult
	testutil.FailErr(t, "decode default wait result", json.Unmarshal([]byte(rawDefault), &resDefault))
	if resDefault.Status != "parked" || resDefault.TimeoutMS != loopwake.DefaultWaitSeconds*1000 {
		t.Fatalf("expected 60000ms default timeout, got %+v", resDefault)
	}

	// until_complete accepts watch_match alongside process_done and resolves omitted timeout_ms to 1800s.
	tctx.Identity.ToolCallID = "call-complete"
	rawComplete, err := invoker.Handler(t.Context(), map[string]any{
		"until_complete": true,
		"conditions": []any{
			map[string]any{"kind": "watch_match", "handles": []any{w1.Handle}},
			map[string]any{"kind": "process_done", "handles": []any{"cmd-known"}},
		},
	}, tctx)
	testutil.FailErr(t, "invoke wait with until_complete and watch_match", err)
	var resComplete loopwake.WaitToolResult
	testutil.FailErr(t, "decode until_complete wait result", json.Unmarshal([]byte(rawComplete), &resComplete))
	if resComplete.Status != "parked" || !resComplete.UntilComplete || resComplete.TimeoutMS != loopwake.MaxWaitSeconds*1000 {
		t.Fatalf("expected 1800000ms until_complete timeout, got %+v", resComplete)
	}

	// Interrupt lease and verify resume restores watch_match and until_complete mode.
	_, err = waitStore.SettleLease(t.Context(), resComplete.LeaseID, "interrupted", awaitstore.Condition{})
	testutil.FailErr(t, "interrupt lease for resume", err)
	loop.Waits.BreakSleep(t.Context(), "session-1", "interrupted", false)

	tctx.Identity.ToolCallID = "call-resume"
	rawResume, err := invoker.Handler(t.Context(), map[string]any{"resume": true}, tctx)
	testutil.FailErr(t, "invoke wait resume", err)
	var resResume loopwake.WaitToolResult
	testutil.FailErr(t, "decode resumed wait result", json.Unmarshal([]byte(rawResume), &resResume))
	if resResume.Status != "parked" || !resResume.UntilComplete || len(resResume.ActiveHandles) != 1 || resResume.ActiveHandles[0] != w1.Handle {
		t.Fatalf("resume lost watch_match or until_complete: %+v", resResume)
	}
}
