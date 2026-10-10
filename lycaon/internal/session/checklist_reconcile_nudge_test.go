package session

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/progress"
)

func pendingReconcileNudge(mgr *Host, rootID string) (string, bool) {
	id := mgr.Coordinator.Runtime.Kicks().TakePendingKickID(rootID)
	return id, anchor.SameInform(id, anchor.ProgressStale)
}

func TestChecklistReconcileNudgeFiresOnOpenSteps(t *testing.T) {
	store := progress.NewMemoryStore()
	store.Set("root-1", "## Progress\n- [x] survey repo\n- [ ] wire the handler\n- [ ] add tests")
	mgr := newClosureGuardManager(t, store)

	mgr.Stops.QueueChecklistReconcileNudge(t.Context(), "root-1")

	if _, ok := pendingReconcileNudge(mgr, "root-1"); !ok {
		t.Fatal("expected progress.stale nudge when open steps remain at interrupt")
	}
}

func TestChecklistReconcileNudgeSilentWhenAllTerminal(t *testing.T) {
	store := progress.NewMemoryStore()
	store.Set("root-1", "## Progress\n- [x] survey repo\n- [~] wire the handler")
	mgr := newClosureGuardManager(t, store)

	mgr.Stops.QueueChecklistReconcileNudge(t.Context(), "root-1")

	if id, ok := pendingReconcileNudge(mgr, "root-1"); ok || id != "" {
		t.Fatalf("no nudge expected when the checklist is fully terminal, got %q", id)
	}
}

func TestChecklistReconcileNudgeSilentWhenProgressMissing(t *testing.T) {
	store := progress.NewMemoryStore()
	mgr := newClosureGuardManager(t, store)

	mgr.Stops.QueueChecklistReconcileNudge(t.Context(), "root-1")

	if id, ok := pendingReconcileNudge(mgr, "root-1"); ok || id != "" {
		t.Fatalf("no nudge expected without a plan, got %q", id)
	}
}

func TestChecklistReconcileNudgeIgnoresOptionalOnlyRemainder(t *testing.T) {
	store := progress.NewMemoryStore()
	store.Set("root-1", "## Progress\n- [x] investigate\n- [>] write up findings")
	mgr := newClosureGuardManager(t, store)

	mgr.Stops.QueueChecklistReconcileNudge(t.Context(), "root-1")

	if id, ok := pendingReconcileNudge(mgr, "root-1"); ok || id != "" {
		t.Fatalf("optional-only remainder must not nudge, got %q", id)
	}
}
