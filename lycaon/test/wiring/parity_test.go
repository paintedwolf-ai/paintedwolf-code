package wiring

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// TestHarnessPostureToolFilterParity verifies production wiring exposes posture rules
// and filters delegate_dispatch from spec coordinator tool lists.
func TestHarnessPostureToolFilterParity(t *testing.T) {
	h := BuildForTest(t, WithRecordingLLM())
	ctx := context.Background()
	dir := t.TempDir()
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{
		Posture: wire.SessionPostureSpec,
	}, dir)
	testutil.FailErr(t, "create session in store", err)
	listed := h.SessionMgr.Coordinator.Guards.Policy().ListForPrompt(ctx, sess, orchestration.ProfileCoordinator)
	for _, meta := range listed {
		if meta.Name == "delegate_dispatch" {
			t.Fatal("spec coordinator must omit delegate_dispatch from listed tools")
		}
	}
	if err := h.SessionMgr.Coordinator.Guards.Policy().EvaluateInvoke(ctx, sess, "delegate_dispatch", nil); err == nil {
		t.Fatal("invoke-time rules must deny delegate_dispatch in spec posture")
	}
}
