package wiring

import (
	"context"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestHandoffReserveVisibleOnPackBoardAndPeerLegWiring(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()

	parent, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create parent", err)

	overlayA := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-a")
	overlayB := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-b")
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
		WorkspaceRoot: overlayB, Scope: &wire.TaskScope{Mode: wire.TaskScopeModeWrite, Paths: []string{"pkg/**"}},
		CreatedAt: spawnAt,
	}
	testutil.FailErr(t, "defaults", worker.ApplyEnqueueDefaults(&taskA, project.ProjectScope{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir}, worker.DefaultWorkersConfig()))
	testutil.FailErr(t, "defaults", worker.ApplyEnqueueDefaults(&taskB, project.ProjectScope{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir}, worker.DefaultWorkersConfig()))
	_, err = h.Delegations.Queue.Enqueue(ctx, taskA)
	testutil.FailErr(t, "enqueue a", err)
	_, err = h.Delegations.Queue.Enqueue(ctx, taskB)
	testutil.FailErr(t, "enqueue b", err)

	childA, err := h.Store.CreateChild(ctx, parent, wire.SpawnChildRequest{AgentType: "implementer", Prompt: "Timer leg"})
	testutil.FailErr(t, "create child a", err)
	childB, err := h.Store.CreateChild(ctx, parent, wire.SpawnChildRequest{AgentType: "implementer", Prompt: "Parser leg"})
	testutil.FailErr(t, "create child b", err)
	testutil.FailErr(t, "link a", h.Delegations.Queue.SetChildSessionID(ctx, taskA.ID, childA.ID))
	testutil.FailErr(t, "link b", h.Delegations.Queue.SetChildSessionID(ctx, taskB.ID, childB.ID))

	_, err = h.ToolRegistry.Run(ctx, "handoff_reserve", map[string]any{
		"paths": []string{"shellsim/builtins.py"},
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

	out, err := h.ToolRegistry.Run(ctx, "pack_board", map[string]any{}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{SessionID: parent.ID},
	})
	testutil.FailErr(t, "pack_board", err)
	if !strings.Contains(out, "shellsim/builtins.py") || !strings.Contains(out, "Reserved paths") {
		t.Fatalf("pack_board missing reservation: %q", out)
	}

	peer := h.Sessions.Manager.PeerReservations(ctx, childB.ID)
	if len(peer) != 1 || peer[0].Path != "shellsim/builtins.py" || peer[0].JobID != "job-a" {
		t.Fatalf("peer reservations = %+v", peer)
	}
	if peer[0].LegLabel != "timer-leg" {
		t.Fatalf("leg label = %q", peer[0].LegLabel)
	}
}

func TestRenderWorkerLegInjectReservedPaths(t *testing.T) {
	ctx := context.Background()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	renderer := prompts.NewInjectRenderer(engine)
	block, err := inject.RenderWorkerLegInject(ctx, renderer, "sess-inject-test", inject.WorkerLegContext{
		LegID:     "leg-b",
		LegTools:  []string{"read"},
		Checklist: []string{"work"},
		ReservedPaths: []inject.ReservedPath{{
			Path: "shellsim/builtins.py", JobID: "job-a", LegLabel: "Timer leg",
		}},
	})
	testutil.FailErr(t, "render", err)
	if !strings.Contains(block, "## Reserved paths") || !strings.Contains(block, "shellsim/builtins.py") {
		t.Fatalf("block = %q", block)
	}
}
