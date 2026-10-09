package runtime_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAssertSessionRunnable(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	if err := mgr.Policy.AssertSessionRunnable(ctx, "sess-1"); err != nil {
		t.Fatalf("running: %v", err)
	}
	if _, err := mgr.Controls.Pause(ctx, run.ID, "test"); err != nil {
		testutil.FailErr(t, "mgr.Pause failed", err)
	}
	if err := mgr.Policy.AssertSessionRunnable(ctx, "sess-1"); err == nil {
		t.Fatal("expected paused session to be blocked")
	}
	if phase := mgr.Policy.CurrentPhase(ctx, "sess-1"); phase != "research" {
		t.Fatalf("phase = %q", phase)
	}
}
