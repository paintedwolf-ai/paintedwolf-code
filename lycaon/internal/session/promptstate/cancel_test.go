package promptstate

import "testing"

func TestCancelRegistrationReplacementAndRelease(t *testing.T) {
	var state State
	var order []string
	state.RegisterCancel("session", func() { order = append(order, "replaced") })
	state.RegisterCancel("session", func() {
		if state.Running("session") {
			t.Error("cancel callback observed an active registration")
		}
		order = append(order, "current")
	})
	if len(order) != 1 || order[0] != "replaced" || !state.Running("session") {
		t.Fatalf("replacement changed cancellation order: %v", order)
	}
	state.Cancel("session")
	state.Cancel("session")
	if len(order) != 2 || order[1] != "current" || state.Running("session") {
		t.Fatalf("cancel did not release exactly once: %v", order)
	}
}

func TestPromptLockFailedTryReleasesReference(t *testing.T) {
	var state State
	first := state.Prompt.Acquire("session")
	first.Lock()
	second := state.Prompt.Acquire("session")
	if second.TryLock() {
		t.Fatal("matching prompt acquired an already-held lock")
	}
	first.Unlock()
	if got := state.Prompt.len(); got != 0 {
		t.Fatalf("failed try retained %d idle lock entries", got)
	}
}
