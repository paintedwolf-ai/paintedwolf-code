package session

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestStopWorkerRuntimePreservesParentAndSibling(t *testing.T) {
	mgr, _ := newTestManager(t)
	parent, err := mgr.store.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create worker parent", err)
	child, err := mgr.store.CreateChild(t.Context(), parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create target worker", err)
	sibling, err := mgr.store.CreateChild(t.Context(), parent, api.SpawnChildRequest{AgentType: "code-reviewer"})
	testutil.FailErr(t, "create sibling worker", err)
	for _, id := range []string{parent.ID, child.ID, sibling.ID} {
		testutil.FailErr(t, "mark active runtime", mgr.store.SetSessionStatus(t.Context(), id, api.SessionStatusBusy))
	}
	released := []string{}
	testutil.FailErr(t, "register process cleanup", mgr.Resources.RegisterCleanup("fixture-processes", 20, func(_ context.Context, id string) error {
		released = append(released, id)
		return nil
	}))
	if err := mgr.Workers.Cancellations.StopRuntime(t.Context(), parent.ID); err == nil {
		t.Fatal("accepted coordinator as worker")
	}
	if len(released) != 0 {
		t.Fatalf("released parent resources: %v", released)
	}
	testutil.FailErr(t, "stop parked worker resources", mgr.Workers.Cancellations.StopRuntime(t.Context(), child.ID))
	if len(released) != 1 || released[0] != child.ID {
		t.Fatalf("released resources=%v", released)
	}
	for _, id := range []string{parent.ID, child.ID, sibling.ID} {
		got, err := mgr.store.Get(t.Context(), id)
		testutil.FailErr(t, "inspect retained session", err)
		want := api.SessionStatusBusy
		if id == child.ID {
			want = api.SessionStatusIdle
		}
		if got.Status != want {
			t.Fatalf("session=%s status=%s want=%s", id, got.Status, want)
		}
	}
}
