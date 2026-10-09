package session_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestParentSessionWorkerCycleIdleWithoutQueue(t *testing.T) {
	idle, err := session.ParentSessionWorkerCycleIdle(t.Context(), nil, testdbseed.DefaultProjectID, "parent-without-workers", "")
	testutil.FailErr(t, "worker cycle without configured queue", err)
	if !idle {
		t.Fatal("a session without a worker queue must remain idle")
	}
}

func TestParentSessionWorkerCycleIdle(t *testing.T) {
	ctx := context.Background()
	q := worker.NewInMemoryQueue(8)
	dir := t.TempDir()
	parent := "parent-1"

	idle, err := session.ParentSessionWorkerCycleIdle(ctx, q, testdbseed.DefaultProjectID, parent, "")
	testutil.FailErr(t, "ParentSessionWorkerCycleIdle", err)
	if !idle {
		t.Fatal("expected idle with no jobs")
	}

	jobID, err := q.Enqueue(ctx, api.WorkerTask{
		ParentSessionID: parent,
		ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: "implementer",
		Prompt:    "work",
		Brief:     "fixture",
		Status:    api.WorkerStatusPending,
	})
	testutil.FailErr(t, "Enqueue", err)

	idle, err = session.ParentSessionWorkerCycleIdle(ctx, q, testdbseed.DefaultProjectID, parent, "")
	testutil.FailErr(t, "ParentSessionWorkerCycleIdle with pending", err)
	if idle {
		t.Fatal("expected not idle with pending job")
	}

	idle, err = session.ParentSessionWorkerCycleIdle(ctx, q, testdbseed.DefaultProjectID, parent, jobID)
	testutil.FailErr(t, "ParentSessionWorkerCycleIdle excluding completing job", err)
	if !idle {
		t.Fatal("expected idle when excluding the completing job still marked pending")
	}
}

func TestParentSessionWorkerCycleWaitsForTerminalOutcomeDelivery(t *testing.T) {
	ctx := context.Background()
	q := worker.NewInMemoryQueue(1)
	parent := "parent-outcome"
	jobID, err := q.Enqueue(ctx, api.WorkerTask{
		ParentSessionID: parent, ProjectID: testdbseed.DefaultProjectID,
		Prompt: "fixture", Brief: "fixture", ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "enqueue", err)
	task, err := q.ClaimNext(ctx, worker.ClaimRequest{ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "claim", err)
	completed, err := q.Complete(ctx, task, api.WorkerResult{Status: "complete"})
	testutil.FailErr(t, "complete", err)
	if !completed {
		t.Fatal("completion lost its claim")
	}
	idle, err := session.ParentSessionWorkerCycleIdle(ctx, q, testdbseed.DefaultProjectID, parent, "")
	testutil.FailErr(t, "idle before delivery", err)
	if idle {
		t.Fatal("terminal result became idle before its parent projection was delivered")
	}
	testutil.FailErr(t, "mark delivered", q.MarkOutcomeDelivered(ctx, jobID))
	idle, err = session.ParentSessionWorkerCycleIdle(ctx, q, testdbseed.DefaultProjectID, parent, "")
	testutil.FailErr(t, "idle after delivery", err)
	if !idle {
		t.Fatal("delivered terminal result still held the worker cycle")
	}
}

func TestObserveCoordinatorTaskInFlightParallelCap(t *testing.T) {
	ctx := context.Background()
	q := worker.NewInMemoryQueue(8)
	dir := t.TempDir()
	sess := &api.Session{
		ID:            "parent-1",
		ProjectID:     testdbseed.DefaultProjectID,
		WorkspacePath: dir,
		Posture:       api.SessionPostureBuild,
		AgentType:     "coordinator",
	}

	deps := session.WorkerCycleGuardDeps{Workers: q}
	readScoutArgs := map[string]any{
		"agent_type": "path-explorer",
		"brief":      testTaskBrief("read scout"),
		"scope":      map[string]any{"mode": "read"},
	}
	cap := spawn.MaxInFlightTaskWorkers

	for i := 0; i < cap; i++ {
		agentType := fmt.Sprintf("agent-%d", i)
		_, err := q.Enqueue(ctx, api.WorkerTask{
			ParentSessionID: sess.ID,
			ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
			AgentType: agentType,
			Prompt:    "work",
			Brief:     "fixture",
			Status:    api.WorkerStatusPending,
		})
		testutil.FailErr(t, "Enqueue job", err)

		gc := oar.NewGuardContext()
		testutil.FailErr(t, "Observe after enqueue", session.ObserveCoordinatorTaskInFlight(ctx, deps, sess, "task", readScoutArgs, gc))
		blocked := evaluateHasCode(t, gc, session.CoordinatorWorkerInFlightCode)
		if i < cap-1 {
			if blocked || gc.Workers.WorkerSpawnBlocked {
				t.Fatalf("expected allow with %d in-flight jobs", i+1)
			}
			continue
		}
		if !blocked || !gc.Workers.WorkerSpawnBlocked {
			t.Fatalf("expected reject at cap with %d in-flight jobs", cap)
		}
	}
}

func TestObserveCoordinatorTaskInFlight(t *testing.T) {
	ctx := context.Background()
	q := worker.NewInMemoryQueue(8)
	dir := t.TempDir()
	sess := &api.Session{
		ID:            "parent-1",
		ProjectID:     testdbseed.DefaultProjectID,
		WorkspacePath: dir,
		Posture:       api.SessionPostureBuild,
		AgentType:     "coordinator",
	}
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfig", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	deps := session.WorkerCycleGuardDeps{
		Workers: q,
	}

	gc := oar.NewGuardContext()
	testutil.FailErr(t, "Observe empty", session.ObserveCoordinatorTaskInFlight(ctx, deps, sess, "task", map[string]any{
		"agent_type": "path-explorer",
		"brief":      testTaskBrief("scout"),
	}, gc))
	if evaluateHasCode(t, gc, session.CoordinatorWorkerInFlightCode) {
		t.Fatal("expected allow with no in-flight jobs")
	}

	for i := 0; i < spawn.MaxInFlightTaskWorkers; i++ {
		_, err = q.Enqueue(ctx, api.WorkerTask{
			ParentSessionID: sess.ID,
			ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
			AgentType: "path-explorer",
			Prompt:    "work",
			Brief:     "fixture",
		})
		testutil.FailErr(t, "Enqueue", err)
	}

	gc = oar.NewGuardContext()
	testutil.FailErr(t, "Observe at cap", session.ObserveCoordinatorTaskInFlight(ctx, deps, sess, "task", map[string]any{
		"agent_type": "implementer",
		"brief":      testTaskBrief("write leg at cap"),
		"scope": map[string]any{
			"mode":  "write",
			"paths": []any{"internal/auth/**"},
		},
	}, gc))
	if !evaluateHasCode(t, gc, session.CoordinatorWorkerInFlightCode) {
		t.Fatal("expected COORDINATOR_WORKER_IN_FLIGHT")
	}
	formatted, err := guidance.NewStaticRejectFormatter(cfg).Format(session.CoordinatorWorkerInFlightCode, gc.RejectData[session.CoordinatorWorkerInFlightCode])
	testutil.FailErr(t, "Format", err)
	if !strings.Contains(formatted, "Rejected:") {
		t.Fatalf("expected formatted reject block: %v", formatted)
	}
}

func TestObserveCoordinatorTaskInFlightAllowsUnderCap(t *testing.T) {
	ctx := context.Background()
	q := worker.NewInMemoryQueue(8)
	dir := t.TempDir()
	sess := &api.Session{
		ID:            "parent-1",
		ProjectID:     testdbseed.DefaultProjectID,
		WorkspacePath: dir,
		Posture:       api.SessionPostureBuild,
		AgentType:     "coordinator",
	}
	deps := session.WorkerCycleGuardDeps{Workers: q}

	_, err := q.Enqueue(ctx, api.WorkerTask{
		ParentSessionID: sess.ID,
		ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: "implementer",
		Prompt:    "work",
		Brief:     "fixture",
	})
	testutil.FailErr(t, "Enqueue", err)

	gc := oar.NewGuardContext()
	testutil.FailErr(t, "Observe under cap", session.ObserveCoordinatorTaskInFlight(ctx, deps, sess, "task", map[string]any{
		"agent_type": "path-explorer",
	}, gc))
	if evaluateHasCode(t, gc, session.CoordinatorWorkerInFlightCode) {
		t.Fatal("expected allow with one in-flight job under cap")
	}
}

func TestShouldNudgeCoordinatorLoopAfterWorkerTask(t *testing.T) {
	ctx := context.Background()
	q := worker.NewInMemoryQueue(8)
	dir := t.TempDir()
	parent := "parent-1"
	mgr := session.NewManager(store.NewMemory(), nil, nil, settings.DefaultSessionLimits())
	mgr.SetWorkerQueue(q)

	if !mgr.ShouldNudgeCoordinatorLoopAfterWorkerTask(ctx, parent, testdbseed.DefaultProjectID, "") {
		t.Fatal("expected true when worker cycle is idle")
	}

	jobID, err := q.Enqueue(ctx, api.WorkerTask{
		ParentSessionID: parent,
		ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: "implementer",
		Prompt:    "work",
		Brief:     "fixture",
	})
	testutil.FailErr(t, "Enqueue", err)

	if mgr.ShouldNudgeCoordinatorLoopAfterWorkerTask(ctx, parent, testdbseed.DefaultProjectID, "") {
		t.Fatal("expected false while jobs pending without completing job id")
	}
	if !mgr.ShouldNudgeCoordinatorLoopAfterWorkerTask(ctx, parent, testdbseed.DefaultProjectID, jobID) {
		t.Fatal("expected true when excluding completing job before queue.Complete")
	}
}

func TestShouldNudgeCoordinatorLoopAfterWorkerTaskWriteDefersUntilIdle(t *testing.T) {
	ctx := context.Background()
	q := worker.NewInMemoryQueue(8)
	dir := t.TempDir()
	parent := "parent-1"
	mgr := session.NewManager(store.NewMemory(), nil, nil, settings.DefaultSessionLimits())
	mgr.SetWorkerQueue(q)

	writeA, err := q.Enqueue(ctx, api.WorkerTask{
		ParentSessionID: parent,
		ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: "implementer",
		Prompt:    "a",
		Brief:     "fixture",
		Scope:     &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.py"}},
	})
	testutil.FailErr(t, "Enqueue write A", err)
	_, err = q.Enqueue(ctx, api.WorkerTask{
		ParentSessionID: parent,
		ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: "implementer",
		Prompt:    "b",
		Brief:     "fixture",
		Scope:     &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"b.py"}},
	})
	testutil.FailErr(t, "Enqueue write B", err)

	if mgr.ShouldNudgeCoordinatorLoopAfterWorkerTask(ctx, parent, testdbseed.DefaultProjectID, writeA) {
		t.Fatal("expected false for write worker wake while sibling write still pending")
	}
}

func TestShouldNudgeCoordinatorLoopAfterWorkerTaskPerJobWakeWhileSiblingsInFlight(t *testing.T) {
	ctx := context.Background()
	q := worker.NewInMemoryQueue(8)
	dir := t.TempDir()
	parent := "parent-1"
	mgr := session.NewManager(store.NewMemory(), nil, nil, settings.DefaultSessionLimits())
	mgr.SetWorkerQueue(q)

	jobA, err := q.Enqueue(ctx, api.WorkerTask{
		ParentSessionID: parent,
		ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: "path-explorer",
		Prompt:    "a",
		Brief:     "fixture",
	})
	testutil.FailErr(t, "Enqueue A", err)
	_, err = q.Enqueue(ctx, api.WorkerTask{
		ParentSessionID: parent,
		ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: "repo-researcher",
		Prompt:    "b",
		Brief:     "fixture",
	})
	testutil.FailErr(t, "Enqueue B", err)

	if !mgr.ShouldNudgeCoordinatorLoopAfterWorkerTask(ctx, parent, testdbseed.DefaultProjectID, jobA) {
		t.Fatal("expected true for per-job wake while sibling job still pending")
	}
}

func TestFormatCoordinatorInFlightRoster(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	roster, err := guidance.RenderWorkerInFlightRoster(context.Background(), guidance.BuildWorkerRosterLines([]api.WorkerTask{
		{ID: "8a3f12ab-cdef", AgentType: "implementer", Status: api.WorkerStatusPending, Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"internal/auth/**"}}},
		{ID: "9c12dead-beef", AgentType: "path-explorer", Status: api.WorkerStatusRunning},
	}))
	if err != nil {
		t.Fatalf("RenderWorkerInFlightRoster: %v", err)
	}
	if !strings.Contains(roster, "8a3f") || !strings.Contains(roster, "implementer") {
		t.Fatalf("roster = %q", roster)
	}
	if !strings.Contains(roster, "9c12") || !strings.Contains(roster, "path-explorer") {
		t.Fatalf("roster = %q", roster)
	}
	if !strings.Contains(roster, "write · focus: internal/auth/**") {
		t.Fatalf("roster = %q", roster)
	}
	if !strings.Contains(roster, "batch dispatch") {
		t.Fatalf("roster = %q", roster)
	}
	if !strings.Contains(roster, "remaining capacity") {
		t.Fatalf("roster = %q", roster)
	}
}

func TestFormatTaskEnqueuedBannerMentionsParallelCap(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	banner, err := guidance.RenderTaskQueuedBanner(context.Background(), guidance.TaskQueuedBannerOpts{
		AgentType: "implementer", JobID: "job-1", MaxInFlight: spawn.MaxInFlightTaskWorkers,
	})
	if err != nil {
		t.Fatalf("RenderTaskQueuedBanner: %v", err)
	}
	want := strconv.Itoa(spawn.MaxInFlightTaskWorkers)
	if !strings.Contains(banner, want) {
		t.Fatalf("expected parallel cap %s in banner: %q", want, banner)
	}
	if !strings.Contains(banner, "BANNER_TASK_QUEUED") {
		t.Fatalf("expected banner code: %q", banner)
	}
}

func TestBuildImplementSessionStateLedgerOverridesStaleEnvelope(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := store.NewMemory()
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create parent", err)

	q := worker.NewInMemoryQueue(4)
	_, err = q.Enqueue(ctx, api.WorkerTask{
		ParentSessionID: parent.ID,
		ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType:     "implementer",
		Prompt:        "done",
		Brief:         "fixture",
		Status:        api.WorkerStatusComplete,
		MergeStatus:   api.WorkerMergeStatusMerged,
		WorkspaceRoot: filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-merged"),
		Scope:         &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.go"}},
	})
	testutil.FailErr(t, "Enqueue merged worker", err)

	staleEnvelope := `<task job_id="job-stale" agent_type="implementer" state="complete" merge_status="pending"><summary>wrote</summary></task>`
	if err := store.AppendMessages(ctx, parent.ID, api.Message{
		Role:    api.MessageRoleAssistant,
		Content: staleEnvelope,
	}); err != nil {
		testutil.FailErr(t, "AppendMessages", err)
	}

	mgr := session.NewManager(store, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetWorkerQueue(q)
	state := mgr.BuildImplementSessionState(ctx, parent)
	if len(state.PendingOverlayIDs) != 0 {
		t.Fatalf("pending = %v want empty when ledger has no pending merges", state.PendingOverlayIDs)
	}

	profile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{},
		parent,
		[]api.Message{
			{Role: api.MessageRoleAssistant, Content: staleEnvelope},
			{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Visibility: api.MessageVisibilityInternal, Kind: api.MessageKindHostLoopWake},
		},
		state,
	)
	if profile.SurfaceID == surface.SurfaceImplementOverlayPromote {
		t.Fatalf("surface = %q want dispatch or synthesis when ledger overrides stale envelope", profile.SurfaceID)
	}
}
