package guidance

import "testing"

func TestFeedbackDedup_ClearSessionResets(t *testing.T) {
	d := NewFeedbackDeduper()
	if !d.ShouldAppendDetails("s1", "h1") {
		t.Fatal("first should append")
	}
	if d.ShouldAppendDetails("s1", "h1") {
		t.Fatal("second same hash should omit")
	}
	d.ClearSession("s1")
	if !d.ShouldAppendDetails("s1", "h1") {
		t.Fatal("after clear, first should append again")
	}
}

func TestFeedbackDedup_EmptySessionIDAlwaysAppends(t *testing.T) {
	d := NewFeedbackDeduper()
	if !d.ShouldAppendDetails("", "h1") {
		t.Fatal("empty session id should always append")
	}
	if !d.ShouldAppendDetails("", "h1") {
		t.Fatal("empty session id should always append on repeat")
	}
}

func TestFeedbackDedup_HashChangeAppendsAgain(t *testing.T) {
	d := NewFeedbackDeduper()
	_ = d.ShouldAppendDetails("s1", "h1")
	if d.ShouldAppendDetails("s1", "h1") {
		t.Fatal("same hash should omit")
	}
	if !d.ShouldAppendDetails("s1", "h2") {
		t.Fatal("hash change should append")
	}
}

func TestFeedbackDedup_MainDebugSwitchAlwaysAppends(t *testing.T) {
	t.Setenv("LYCAON_TOOL_FEEDBACK_VERBOSE", "")
	t.Setenv("LYCAON_DEBUG_ALL", "1")
	d := NewFeedbackDeduper()
	// The main switch forces every reject to append the full checklist,
	// even a repeat with the same hash that would normally dedupe.
	if !d.ShouldAppendDetails("s1", "h1") {
		t.Fatal("main switch should append")
	}
	if !d.ShouldAppendDetails("s1", "h1") {
		t.Fatal("main switch should append on repeat")
	}
}

// TestFeedbackDeduperClearSessionReleasesState verifies session cleanup clears dedupe state.
func TestFeedbackDeduperClearSessionReleasesState(t *testing.T) {
	f := NewToolRejectFormatter(nil)

	if !f.shouldAppendDetails("s1", "hash-a") {
		t.Fatal("first reject must append the checklist")
	}
	if f.shouldAppendDetails("s1", "hash-a") {
		t.Fatal("an unchanged checklist must not append twice")
	}

	f.ForgetSession("s1")

	if !f.shouldAppendDetails("s1", "hash-a") {
		t.Fatal("after ForgetSession the session must behave as new")
	}
}
