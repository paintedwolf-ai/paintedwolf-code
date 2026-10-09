package wiring

import (
	"context"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCoordinatorParallelTaskCapMixedAgents(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, dir)
	testutil.FailErr(t, "create session", err)

	q := h.WorkerQueue
	deps := session.WorkerCycleGuardDeps{Workers: q}
	agents := []string{"repo-researcher", "path-explorer", "implementer", "code-reviewer", "web-researcher"}
	cap := spawn.MaxInFlightTaskWorkers

	for i := 0; i < cap; i++ {
		agentType := agents[i%len(agents)]
		gc := observeReadScoutSpawn(t, ctx, deps, sess, []any{"internal/**"})
		if _, rejected := gc.RejectData[session.CoordinatorWorkerInFlightCode]; rejected || gc.Workers.WorkerSpawnBlocked {
			t.Fatalf("expected allow before enqueuing %s at %d in flight", agentType, i)
		}
		_, err = q.Enqueue(ctx, api.WorkerTask{
			ParentSessionID: sess.ID,
			ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
			AgentType: agentType,
			Prompt:    fmt.Sprintf("parallel leg %d", i),
			Brief:     "fixture",
			Status:    api.WorkerStatusPending,
		})
		testutil.FailErr(t, "enqueue worker", err)
	}

	gc := observeReadScoutSpawn(t, ctx, deps, sess, []any{"internal/**"})
	if !gc.Workers.WorkerSpawnBlocked || gc.Workers.ActiveWorkerCount != int64(cap) {
		t.Fatalf("expected spawn blocked with %d mixed agents in flight; blocked=%v active=%d", cap, gc.Workers.WorkerSpawnBlocked, gc.Workers.ActiveWorkerCount)
	}
	if _, observed := gc.RejectData[session.CoordinatorWorkerInFlightCode]; !observed {
		t.Fatalf("guard did not stamp reject data at cap; reject data = %v", gc.RejectData)
	}
}

// observeReadScoutSpawn runs the coordinator task() guard for a read scout.
func observeReadScoutSpawn(t *testing.T, ctx context.Context, deps session.WorkerCycleGuardDeps, sess *api.Session, paths []any) *oar.GuardContext {
	t.Helper()
	gc := oar.NewGuardContext()
	testutil.FailErr(t, "ObserveCoordinatorTaskInFlight", session.ObserveCoordinatorTaskInFlight(ctx, deps, sess, "task", map[string]any{
		"agent_type": "path-explorer",
		"brief":      map[string]any{"goal": "read scout", "done_when": []any{"Return grounded results."}},
		"scope":      map[string]any{"mode": "read", "paths": paths},
	}, gc))
	return gc
}
