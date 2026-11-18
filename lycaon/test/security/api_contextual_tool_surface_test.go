package security

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestSpecCoordinatorPromptOmitsPlanFromBlueprint(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithRecordingLLM())
	srv := h.Server
	rec := h.Recording
	projectDir := t.TempDir()
	sess := createSessionWithPostureHTTP(t, srv, projectDir, wire.SessionPostureSpec)

	postPromptAndWaitIdle(t, srv, sess.ID, "review plan scope")
	for _, tool := range rec.LastRequest().Tools {
		if tool.Name == "plan_from_blueprint" {
			t.Fatalf("spec coordinator prompt must omit plan_from_blueprint, got %d tools", len(rec.LastRequest().Tools))
		}
	}
}

func TestInvokeStillDeniesWhenToolNotListed(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithRecordingLLM())
	srv := h.Server
	mgr := h.Sessions.Manager
	store := h.Store
	projectDir := t.TempDir()
	sess := createSessionWithPostureHTTP(t, srv, projectDir, wire.SessionPostureSpec)
	ctx := context.Background()
	sessionRec, err := store.Sessions.Get(ctx, sess.ID)
	testutil.FailErr(t, "store.Get failed", err)
	if err := mgr.Coordinator.Guards.Policy().EvaluateInvoke(ctx, sessionRec, "plan_from_blueprint", nil); err == nil {
		t.Fatal("invoke-time rules must still deny plan_from_blueprint in spec posture")
	}
}
