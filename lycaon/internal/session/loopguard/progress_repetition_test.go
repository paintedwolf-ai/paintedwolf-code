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
