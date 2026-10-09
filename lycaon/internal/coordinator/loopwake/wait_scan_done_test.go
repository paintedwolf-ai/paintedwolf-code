package loopwake

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
)

func TestResolveConditionsAcceptsScanDone(t *testing.T) {
	subscription, err := resolveConditions([]any{map[string]any{"kind": "scan_done"}})
	if err != nil {
		t.Fatalf("resolveConditions: %v", err)
	}
	if !hasWaitTriggerInSlice(subscription.Triggers, WaitTriggerScanDone) {
		t.Fatalf("triggers = %v want scan_done", subscription.Triggers)
	}
}

func TestDefaultSubscriptionsExcludeScanDone(t *testing.T) {
	if hasWaitTriggerInSlice(DefaultCoordinatorWaitTriggers(true), WaitTriggerScanDone) {
		t.Fatal("default wait set must not include scan_done")
	}
}

func TestWaitEventMatchesScanDoneRequiresSubscription(t *testing.T) {
	subscribed := []WaitTrigger{WaitTriggerTimer, WaitTriggerScanDone}
	if !waitEventMatches(subscribed, waitMatchInput{Wake: anchor.ScanFinished}) {
		t.Fatal("scan_done nudge must wake a scan_done subscription")
	}
	if waitEventMatches([]WaitTrigger{WaitTriggerTimer}, waitMatchInput{Wake: anchor.ScanFinished}) {
		t.Fatal("scan_done nudge must not wake without subscription")
	}
	if waitEventMatches(nil, waitMatchInput{Wake: anchor.ScanFinished}) {
		t.Fatal("scan_done nudge must not wake the default wait set")
	}
}

func TestSessionsSleepingOnScanDone(t *testing.T) {
	loop := NewLoopEngine()
	deadline := time.Now().UTC().Add(10 * time.Minute)
	loop.Waits.EnterSleep(context.Background(), "scan-waiter", deadline, "waiting for scan", []WaitTrigger{WaitTriggerTimer, WaitTriggerScanDone}, nil, SleepMoverHost)
	loop.Waits.EnterSleep(context.Background(), "worker-waiter", deadline, "waiting for workers", DefaultCoordinatorWaitTriggers(false), nil, SleepMoverHost)

	got := loop.Waits.SessionsSleepingOn(WaitTriggerScanDone)
	if len(got) != 1 || got[0] != "scan-waiter" {
		t.Fatalf("SessionsSleepingOn = %v want [scan-waiter]", got)
	}

	loop.Waits.breakSleep(t.Context(), "scan-waiter", "scan_done", true)
	if got := loop.Waits.SessionsSleepingOn(WaitTriggerScanDone); len(got) != 0 {
		t.Fatalf("after break SessionsSleepingOn = %v want empty", got)
	}
}

func TestNudgeScanFinishedBreaksSubscribedSleepOnly(t *testing.T) {
	loop := NewLoopEngine()
	deps := loopDepsForTest()
	deps.GetSession = func(_ context.Context, sessionID string) (*api.Session, error) {
		return &api.Session{ID: sessionID, Status: api.SessionStatusIdle}, nil
	}
	loop.SetDeps(deps)
	deadline := time.Now().UTC().Add(10 * time.Minute)
	loop.Waits.EnterSleep(context.Background(), "scan-waiter", deadline, "waiting for scan", []WaitTrigger{WaitTriggerTimer, WaitTriggerScanDone}, nil, SleepMoverHost)
	loop.Waits.EnterSleep(context.Background(), "worker-waiter", deadline, "waiting for workers", DefaultCoordinatorWaitTriggers(false), nil, SleepMoverHost)

	loop.Nudges.NudgeScanFinished(context.Background(), "scan-waiter", "scan-1", anchor.Envelope{})
	loop.Nudges.NudgeScanFinished(context.Background(), "worker-waiter", "scan-1", anchor.Envelope{})

	if loop.Waits.IsSleeping("scan-waiter") {
		t.Fatal("scan_done nudge must break a subscribed sleep")
	}
	if !loop.Waits.IsSleeping("worker-waiter") {
		t.Fatal("scan_done nudge must not break an unsubscribed sleep")
	}
}

func scanCycleLoopDeps(open bool) LoopDeps {
	deps := idleWaitLoopDeps()
	deps.ScanCycleOpen = func(context.Context, string) bool { return open }
	return deps
}

func TestWaitScanDoneAlreadySatisfiedWhenNoOpenScans(t *testing.T) {
	loop := NewLoopEngine()
	loop.SetDeps(scanCycleLoopDeps(false))
	reg := tools.NewDefaultRegistry()
	if err := RegisterWaitTool(reg, loop.Subscriptions, WaitToolDeps{}); err != nil {
		t.Fatalf("RegisterWaitTool: %v", err)
	}
	out, err := reg.Run(context.Background(), "wait", map[string]any{
		"conditions": []any{map[string]any{"kind": "scan_done"}},
	}, tools.ToolContext{
		SessionID: "s1",
		Agent:     orchestration.ProfileCoordinator,
	})
	if err != nil {
		t.Fatalf("wait(scan_done) with no open scans: %v", err)
	}
	var result WaitToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Status != "scan_done" {
		t.Fatalf("status = %q want scan_done", result.Status)
	}
	if loop.Waits.IsSleeping("s1") {
		t.Fatal("must not arm sleep when no scans are open")
	}
}

func TestWaitScanDoneArmsSleepWhileScanOpen(t *testing.T) {
	loop := NewLoopEngine()
	loop.SetDeps(scanCycleLoopDeps(true))
	reg := tools.NewDefaultRegistry()
	if err := RegisterWaitTool(reg, loop.Subscriptions, WaitToolDeps{}); err != nil {
		t.Fatalf("RegisterWaitTool: %v", err)
	}
	invocationOut := &tools.ToolInvocationOut{}
	out, err := reg.Run(context.Background(), "wait", map[string]any{
		"timeout_ms": 180000,
		"conditions": []any{map[string]any{"kind": "scan_done"}},
		"reason":     "waiting for SAST scan to complete",
	}, tools.ToolContext{
		SessionID: "s1",
		Agent:     orchestration.ProfileCoordinator,
		Out:       invocationOut,
	})
	if err != nil {
		t.Fatalf("wait(scan_done) with open scan: %v", err)
	}
	if !WaitCompletionEndsCycle("wait", invocationOut.Completion) {
		t.Fatalf("output = %q", out)
	}
	if !loop.Waits.IsSleeping("s1") {
		t.Fatal("expected sleeping while scan is open")
	}
	if !hasWaitTrigger(waitSubscriptionForTest(loop.Subscriptions, "s1"), WaitTriggerScanDone) {
		t.Fatalf("armed triggers = %v want scan_done", waitSubscriptionForTest(loop.Subscriptions, "s1"))
	}
}
