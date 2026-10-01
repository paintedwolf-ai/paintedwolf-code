package loopwake

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
)

// Wait subscription invariants: trigger parsing edges, match matrix exhaustiveness,
// always-wake overrides, defer logic, and dedup stability.

func TestResolveConditionsRejectsUnknownKind(t *testing.T) {
	_, err := resolveConditions([]any{map[string]any{"kind": "purple"}})
	if err == nil {
		t.Fatal("expected error for unknown trigger")
	}
}

func TestResolveConditionsRejectsNonObjectEntry(t *testing.T) {
	_, err := resolveConditions([]any{42})
	if err == nil {
		t.Fatal("expected error for non-string entry")
	}
}

func TestResolveConditionsRejectsNonArray(t *testing.T) {
	_, err := resolveConditions("timer")
	if err == nil {
		t.Fatal("expected error for non-array")
	}
}

func TestResolveConditionsAcceptsObjectArray(t *testing.T) {
	subscription, err := resolveConditions([]any{map[string]any{"kind": "next_worker_done"}})
	if err != nil {
		t.Fatalf("resolveConditions: %v", err)
	}
	want := []WaitTrigger{WaitTriggerTimer, WaitTriggerNextWorkerDone}
	if !reflect.DeepEqual(subscription.Triggers, want) {
		t.Fatalf("triggers=%v want=%v", subscription.Triggers, want)
	}
}

func TestResolveConditionsEmptyArrayFallsBackToTimer(t *testing.T) {
	subscription, err := resolveConditions([]any{})
	if err != nil {
		t.Fatalf("resolveConditions: %v", err)
	}
	if !reflect.DeepEqual(subscription.Triggers, []WaitTrigger{WaitTriggerTimer}) {
		t.Fatalf("empty conditions should default to timer, got %v", subscription.Triggers)
	}
}

func TestResolveConditionsPreservesDeclaredTriggers(t *testing.T) {
	subscription, err := resolveConditions([]any{
		map[string]any{"kind": "next_worker_done"},
		map[string]any{"kind": "scan_done"},
	})
	if err != nil {
		t.Fatalf("resolveConditions: %v", err)
	}
	if len(subscription.Triggers) != 3 || subscription.Triggers[0] != WaitTriggerTimer || subscription.Triggers[1] != WaitTriggerNextWorkerDone || subscription.Triggers[2] != WaitTriggerScanDone {
		t.Fatalf("triggers = %v", subscription.Triggers)
	}
}

func TestWaitEventMatchesTimerTrigger(t *testing.T) {
	triggers := []WaitTrigger{WaitTriggerTimer}
	if !waitEventMatches(triggers, waitMatchInput{Wake: anchor.WaitTimerFired}) {
		t.Fatal("anchor.WaitTimerFired must match WaitTriggerTimer")
	}
	if waitEventMatches([]WaitTrigger{WaitTriggerNextWorkerDone}, waitMatchInput{Wake: anchor.WaitTimerFired}) {
		t.Fatal("anchor.WaitTimerFired must not match next_worker_done")
	}
}

func TestWaitEventMatchesOverlayPromoteDue(t *testing.T) {
	triggers := []WaitTrigger{WaitTriggerOverlayPromote}
	if !waitEventMatches(triggers, waitMatchInput{Wake: anchor.WorkerTaskFinished, OverlayPromoteDue: true}) {
		t.Fatal("overlay_promote_pending must match when OverlayPromoteDue is true")
	}
	if waitEventMatches(triggers, waitMatchInput{Wake: anchor.WorkerTaskFinished, OverlayPromoteDue: false}) {
		t.Fatal("overlay_promote_pending must not match when no pending promote")
	}
}

func TestWaitEventMatchesNonWorkerNudgeAlwaysMatches(t *testing.T) {
	// Other loop nudges (e.g. PhaseAdvanced, CompactionResume, GateBlocked) are not
	// gated by the trigger subscription matrix — they always wake the loop.
	if !waitEventMatches([]WaitTrigger{WaitTriggerTimer}, waitMatchInput{Wake: anchor.PhaseAdvanced}) {
		t.Fatal("phase advance must always match")
	}
	if !waitEventMatches([]WaitTrigger{}, waitMatchInput{Wake: anchor.PhaseAdvanced}) {
		t.Fatal("empty triggers default to full subscription and must match phase advance")
	}
}

func TestWaitEventMatchesLegFinishedRoutedLikeWorkerDone(t *testing.T) {
	triggers := []WaitTrigger{WaitTriggerNextWorkerDone}
	if !waitEventMatches(triggers, waitMatchInput{
		Wake:            anchor.LegFinished,
		CompletingJobID: "job-1",
	}) {
		t.Fatal("leg-finished with a completing job must match next_worker_done")
	}
}

func TestShouldDeferForAllWorkersIdleAllWorkersIdleOnly(t *testing.T) {
	// All-workers-idle-only subscription with a per-job completion → defer until idle.
	triggers := []WaitTrigger{WaitTriggerAllWorkersIdle}
	if !shouldDeferForAllWorkersIdle(triggers, anchor.WorkerTaskFinished, "job-1") {
		t.Fatal("expected defer for all_workers_idle-only subscription")
	}
}

func TestShouldDeferForAllWorkersIdleNextWorkerSubscribed(t *testing.T) {
	// Both all-workers-idle and next-worker-done subscribed → don't defer, the per-job wake fires.
	triggers := []WaitTrigger{WaitTriggerAllWorkersIdle, WaitTriggerNextWorkerDone}
	if shouldDeferForAllWorkersIdle(triggers, anchor.WorkerTaskFinished, "job-1") {
		t.Fatal("must not defer when next_worker_done also subscribed")
	}
}

func TestShouldDeferForAllWorkersIdleWrongNudge(t *testing.T) {
	// Timer trigger has nothing to do with the all-workers-idle defer policy.
	if shouldDeferForAllWorkersIdle([]WaitTrigger{WaitTriggerAllWorkersIdle}, anchor.WaitTimerFired, "") {
		t.Fatal("timer nudge must not engage the all_workers_idle defer")
	}
}

func TestIsAlwaysWakeInformCoversCriticalIDs(t *testing.T) {
	// These informs bypass the wait subscription matrix because dropping them would
	// leave the coordinator stuck.
	for _, id := range []anchor.ID{
		anchor.GateBlocked,
		anchor.FeedbackPending,
		anchor.FeedbackReceived,
		anchor.ComposeDone,
		anchor.PhaseAdvanced,
	} {
		if !isAlwaysWakeInform(id) {
			t.Errorf("inform %q must be always-wake", id)
		}
	}
}

func TestIsAlwaysWakeInformRejectsUnknown(t *testing.T) {
	for _, id := range []anchor.ID{"", "  ", anchor.WorkerTaskFinished, anchor.LegFinished, anchor.WaitTimerFired} {
		if isAlwaysWakeInform(id) {
			t.Errorf("inform %q should not be always-wake", id)
		}
	}
}

func TestIsAlwaysWakeNudgeOnlyPhaseAdvanced(t *testing.T) {
	if !isAlwaysWakeNudge(anchor.PhaseAdvanced) {
		t.Fatal("PhaseAdvanced must be always-wake")
	}
	for _, n := range []anchor.ID{anchor.WaitTimerFired, anchor.WorkerTaskFinished, anchor.LegFinished} {
		if isAlwaysWakeNudge(n) {
			t.Errorf("nudge %q should not be always-wake", n)
		}
	}
}

func TestWaitTriggerSetEmptyIsEmpty(t *testing.T) {
	set := waitTriggerSet(nil)
	if len(set) != 0 {
		t.Fatalf("empty triggers must stay empty (no DefaultCoordinatorWaitTriggers expand), got %d entries", len(set))
	}
	set = waitTriggerSet([]WaitTrigger{})
	if len(set) != 0 {
		t.Fatalf("empty slice must stay empty, got %d entries", len(set))
	}
}

func TestAllValidWaitTriggersEnumerated(t *testing.T) {
	// Lock the public WaitTrigger enum so adding a value forces an update here.
	want := map[WaitTrigger]struct{}{
		WaitTriggerTimer:          {},
		WaitTriggerNextWorkerDone: {},
		WaitTriggerAllWorkersIdle: {},
		WaitTriggerOverlayPromote: {},
		WaitTriggerScanDone:       {},
		WaitTriggerProcessDone:    {},
		WaitTriggerHTTPReady:      {},
		WaitTriggerPortReady:      {},
	}
	if !reflect.DeepEqual(validWaitTriggers, want) {
		t.Fatalf("validWaitTriggers drifted from expected enum")
	}
}
