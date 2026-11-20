package wiring

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type handoffReservation struct {
	h      *Harness
	dir    string
	parent *wire.Session
	childB *wire.Session
	jobB   string
}

// reservedHandoff runs two implementer legs under one coordinator session and
// reserves reservedPath from the first leg.
func reservedHandoff(t *testing.T) handoffReservation {
	t.Helper()
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()

	parent, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create parent", err)

	overlayA := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-a")
	spawnAt := time.Now().UTC()

	taskA := wire.WorkerTask{
		ID: "job-a", ParentSessionID: parent.ID, ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: "implementer", Prompt: "Timer leg", Brief: "Timer leg", Status: wire.WorkerStatusRunning,
		WorkspaceRoot: overlayA, Scope: &wire.TaskScope{Mode: wire.TaskScopeModeWrite, Paths: []string{"shellsim/**"}},
		CreatedAt: spawnAt, LegID: "timer-leg",
	}
	taskB := wire.WorkerTask{
		ID: "job-b", ParentSessionID: parent.ID, ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: "implementer", Prompt: "Parser leg", Brief: "Parser leg", Status: wire.WorkerStatusRunning,
		Scope:     &wire.TaskScope{Mode: wire.TaskScopeModeWrite, Paths: []string{"pkg/**"}},
		CreatedAt: spawnAt,
	}
	testutil.FailErr(t, "defaults", worker.ApplyEnqueueDefaults(&taskA, project.ProjectScope{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir}, worker.DefaultWorkersConfig()))
	testutil.FailErr(t, "defaults", worker.ApplyEnqueueDefaults(&taskB, project.ProjectScope{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir}, worker.DefaultWorkersConfig()))
	_, err = h.Workers.Queue.Enqueue(ctx, taskA)
	testutil.FailErr(t, "enqueue a", err)
	_, err = h.Workers.Queue.Enqueue(ctx, taskB)
	testutil.FailErr(t, "enqueue b", err)

	childA, err := h.Store.CreateChild(ctx, parent, wire.SpawnChildRequest{AgentType: "implementer", Prompt: "Timer leg"})
	testutil.FailErr(t, "create child a", err)
	childB, err := h.Store.CreateChild(ctx, parent, wire.SpawnChildRequest{AgentType: "implementer", Prompt: "Parser leg"})
	testutil.FailErr(t, "create child b", err)
	testutil.FailErr(t, "link a", h.Workers.Queue.SetChildSessionID(ctx, taskA.ID, childA.ID))
	testutil.FailErr(t, "link b", h.Workers.Queue.SetChildSessionID(ctx, taskB.ID, childB.ID))

	_, err = h.ToolRegistry.Run(ctx, "handoff_reserve", map[string]any{
		"paths": []string{reservedPath},
	}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{SessionID: childA.ID,
			ParentSessionID:  parent.ID,
			HandoffSessionID: parent.ID,
			HandoffAgentID:   taskA.ID,
			WorkerJobID:      taskA.ID},
	})
	testutil.FailErr(t, "handoff_reserve", err)
	return handoffReservation{h: h, dir: dir, parent: parent, childB: childB, jobB: taskB.ID}
}

const reservedPath = "shellsim/builtins.py"

func TestHandoffReserveVisibleOnPackBoardAndPeerLegWiring(t *testing.T) {
	f := reservedHandoff(t)
	h, ctx, dir, parent, childB := f.h, context.Background(), f.dir, f.parent, f.childB

	out, err := h.ToolRegistry.Run(ctx, "pack_board", map[string]any{}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{SessionID: parent.ID},
	})
	testutil.FailErr(t, "pack_board", err)
	if !strings.Contains(out, reservedPath) || !strings.Contains(out, "Reserved paths") {
		t.Fatalf("pack_board missing reservation: %q", out)
	}

	peer := h.Sessions.Manager.Workers.Workspaces.PeerReservations(ctx, childB.ID)
	if len(peer) != 1 || peer[0].Path != reservedPath {
		t.Fatalf("peer reservations = %+v", peer)
	}
	if peer[0].LegLabel != "timer-leg" {
		t.Fatalf("leg label = %q", peer[0].LegLabel)
	}
}

// The served graph binds the board builder, renderer, worker roots and
// reservation source to the coordinator runtime: a coordinator turn and a
// worker request each receive a pack board carrying the reserved path.
func TestBoardInjectReachesCoordinatorAndWorkerTurnsWiring(t *testing.T) {
	f := reservedHandoff(t)
	ctx := context.Background()
	engine := f.h.Sessions.Manager.Coordinator.Runtime.Board()

	block, ok := engine.PrependBoardIfChanged(ctx, f.parent, wire.CoordinatorRunContext{RunID: "run-1"})
	if !ok {
		t.Fatal("coordinator turn received no board inject")
	}
	if !strings.Contains(block, packboard.PackBoardSentinel) || !strings.Contains(block, reservedPath) {
		t.Fatalf("coordinator board inject lacks the pack board or reservation: %q", block)
	}

	// The second leg gets a real private branch; its board resolves the
	// branch roots for the job its request runs.
	_, err := f.h.Workers.Queue.ClaimWorkerBranch(ctx, f.jobB)
	testutil.FailErr(t, "claim worker branch", err)
	workerBlock, ok := engine.WorkerBoard(workercontext.WithJob(ctx, f.jobB), f.childB)
	if !ok {
		t.Fatal("worker request received no board inject")
	}
	if !strings.Contains(workerBlock, packboard.PackBoardSentinel) || !strings.Contains(workerBlock, reservedPath) {
		t.Fatalf("worker board inject lacks the pack board or reservation: %q", workerBlock)
	}
}
