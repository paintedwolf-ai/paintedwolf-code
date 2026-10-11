package turnclock

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCheckpointWaitIsActiveButNotWorkTime(t *testing.T) {
	sessions := store.NewMemory()
	clocks := New(sessions)
	ctx := t.Context()
	root, err := sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create root", err)
	child, err := sessions.CreateChild(ctx, root, api.SpawnChildRequest{AgentType: orchestration.ProfilePathExplorer})
	testutil.FailErr(t, "create child", err)
	t.Cleanup(func() { progress.ForgetClock(root.ID) })

	finish := clocks.Begin(ctx, child.ID, false)
	resume := clocks.Wait(ctx, child.ID)
	time.Sleep(40 * time.Millisecond)
	resume()
	finish()
	clock := progress.Clock(root.ID)
	if clock.ActiveMs < 40 || clock.ActiveMs-clock.WorkMs < 30 {
		t.Fatalf("clock = %+v, want the child's approval wait counted as active but not work", clock)
	}
}

func TestTurnClockOpensOnlyForNewRootUserTurn(t *testing.T) {
	sessions := store.NewMemory()
	clocks := New(sessions)
	ctx := t.Context()
	root, err := sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create root", err)
	child, err := sessions.CreateChild(ctx, root, api.SpawnChildRequest{AgentType: orchestration.ProfilePathExplorer})
	testutil.FailErr(t, "create child", err)
	t.Cleanup(func() { progress.ForgetClock(root.ID) })

	finish := clocks.Begin(ctx, root.ID, true)
	time.Sleep(20 * time.Millisecond)
	finish()
	banked := progress.Clock(root.ID).ActiveMs
	if banked <= 0 {
		t.Fatal("first user turn should bank elapsed time")
	}
	for _, continuation := range []struct {
		id          string
		newUserTurn bool
	}{
		{root.ID, false},
		{child.ID, true},
	} {
		finish = clocks.Begin(ctx, continuation.id, continuation.newUserTurn)
		if got := progress.Clock(root.ID); got.ActiveMs < banked || !got.Running() {
			t.Fatalf("continuation clock = %+v, want at least %d and running", got, banked)
		}
		finish()
	}

	finish = clocks.Begin(ctx, root.ID, true)
	defer finish()
	if got := progress.Clock(root.ID); got.ActiveMs != 0 || !got.Running() {
		t.Fatalf("next user turn clock = %+v, want running from zero", got)
	}
}
