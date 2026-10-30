package loopguard

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProgressNoOpHistorySurvivesUnrelatedMutations(t *testing.T) {
	guard := NewMemoryDoomLoopGuard()
	args := map[string]any{"content": "- [ ] Deliver change"}
	for n := 0; n < DoomLoopMaxAttempts; n++ {
		testutil.FailErr(t, "record no-op", guard.RecordAttempt(t.Context(), "session", fmt.Sprint(n), "update_progress", args, nil, false))
		testutil.FailErr(t, "record unrelated mutation", guard.RecordAttempt(t.Context(), "session", fmt.Sprint(n, "-edit"), "edit", map[string]any{"path": fmt.Sprint(n)}, nil, true))
	}
	allowed, _, _, err := guard.Check(t.Context(), "session", "next", "update_progress", args, true, nil)
	testutil.FailErr(t, "check repeated progress", err)
	if allowed {
		t.Fatal("unrelated mutations erased progress no-op history")
	}
	allowed, _, _, err = guard.Check(t.Context(), "session", "next", "update_progress", map[string]any{"content": "- [x] Deliver change"}, true, nil)
	testutil.FailErr(t, "check changed progress", err)
	if !allowed {
		t.Fatal("repetition guard prevented a deliverable transition")
	}
}

func TestActualProgressChangeInvalidatesAnOlderNoOpContext(t *testing.T) {
	guard := NewMemoryDoomLoopGuard()
	args := map[string]any{"content": "- [ ] Deliver"}
	for n := 0; n < DoomLoopMaxAttempts; n++ {
		testutil.FailErr(t, "record no-op", guard.RecordAttempt(t.Context(), "session", fmt.Sprint(n), "update_progress", args, nil, false))
	}
	testutil.FailErr(t, "record changed progress", guard.RecordAttempt(t.Context(), "session", "changed", "update_progress", map[string]any{"content": "- [x] Deliver"}, nil, true))
	allowed, _, _, err := guard.Check(t.Context(), "session", "new-context", "update_progress", args, true, nil)
	testutil.FailErr(t, "check new progress context", err)
	if !allowed {
		t.Fatal("changed progress retained an obsolete no-op decision")
	}
}

func TestLabelOnlyProgressDoesNotEraseRepeatedNoOpHistory(t *testing.T) {
	guard := NewMemoryDoomLoopGuard()
	args := map[string]any{"content": "- [ ] Deliver"}
	for n := 0; n < DoomLoopMaxAttempts; n++ {
		testutil.FailErr(t, "record repeated no-op", guard.RecordAttempt(t.Context(), "session", fmt.Sprint(n), "update_progress", args, nil, false))
		testutil.FailErr(t, "record bookkeeping rewrite", guard.RecordAttempt(t.Context(), "session", fmt.Sprint(n, "-label"), "update_progress", map[string]any{"content": fmt.Sprintf("- [ ] Deliver detail %d", n)}, nil, false))
	}
	allowed, _, _, err := guard.Check(t.Context(), "session", "next", "update_progress", args, true, nil)
	testutil.FailErr(t, "check unchanged deliverable context", err)
	if allowed {
		t.Fatal("label-only rewrites erased repeated no-op history")
	}
}
