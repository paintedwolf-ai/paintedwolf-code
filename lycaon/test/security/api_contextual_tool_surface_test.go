package security

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestSpecCoordinatorPromptOmitsDelegateDispatch(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithRecordingLLM())
	srv := h.Server
	rec := h.Recording
	projectDir := t.TempDir()
	sess := createSessionWithPostureHTTP(t, srv, projectDir, wire.SessionPostureSpec)

	postPromptAndWaitIdle(t, srv, sess.ID, "review plan scope")
	for _, tool := range rec.LastRequest().Tools {
		if tool.Name == "delegate_dispatch" {
			t.Fatalf("spec coordinator prompt must omit delegate_dispatch, got %d tools", len(rec.LastRequest().Tools))
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
	sessionRec, err := store.Get(ctx, sess.ID)
	testutil.FailErr(t, "store.Get failed", err)
	if err := mgr.Guards.Policy().EvaluateInvoke(ctx, sessionRec, "delegate_dispatch", nil); err == nil {
		t.Fatal("invoke-time rules must still deny delegate_dispatch in spec posture")
	}
}
