package wiring

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCoordinatorTaskScopeDispatchReadFanOutParallelWriteAllowed(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, dir)
	testutil.FailErr(t, "create session", err)

	q := h.WorkerQueue
	capDeps := workeradmission.WorkerCycleGuardDeps{
		Workers:         q,
		MaxWorkers:      func(context.Context, string) int { return 3 },
		MaxReadWorkers:  func(context.Context, string) int { return 3 },
		MaxWriteWorkers: func(context.Context, string) int { return 1 },
	}
	readScopes := []api.TaskScope{
		{Mode: api.TaskScopeModeRead, Paths: []string{"internal/scout-a/**"}},
		{Mode: api.TaskScopeModeRead, Paths: []string{"internal/scout-b/**"}},
		{Mode: api.TaskScopeModeRead, Paths: []string{"internal/scout-c/**"}},
	}
	for i, scope := range readScopes {
		gc := observeReadScoutSpawn(t, ctx, capDeps, sess, []any{scope.Paths[0]})
		if _, rejected := gc.RejectData[workeradmission.CoordinatorWorkerInFlightCode]; rejected || gc.WorkerSpawnBlocked {
			t.Fatalf("expected read scout %d allowed", i+1)
		}
		s := scope
		_, err = q.Enqueue(ctx, api.WorkerTask{
			ParentSessionID: sess.ID,
			ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
			AgentType: "path-explorer",
			Prompt:    "scout",
			Brief:     "fixture",
			Scope:     &s,
			Status:    api.WorkerStatusPending,
		})
		testutil.FailErr(t, "enqueue read scout", err)
	}
	gc := observeReadScoutSpawn(t, ctx, capDeps, sess, []any{readScopes[0].Paths[0]})
	if !gc.Workers.WorkerSpawnBlocked || gc.Workers.MaxWorkers != 3 || gc.Workers.ActiveWorkerCount != 3 {
		t.Fatalf("expected reject at total cap after three read scouts; blocked=%v active=%d max=%d", gc.Workers.WorkerSpawnBlocked, gc.Workers.ActiveWorkerCount, gc.Workers.MaxWorkers)
	}
	if _, observed := gc.RejectData[workeradmission.CoordinatorWorkerInFlightCode]; !observed {
		t.Fatalf("guard did not stamp reject data at total cap; reject data = %v", gc.RejectData)
	}

	_, err = guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfig", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	deps := workeradmission.WorkerCycleGuardDeps{
		Workers: worker.NewInMemoryQueue(8),
	}
	q2 := worker.NewInMemoryQueue(8)
	deps.Workers = q2
	writeScope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"internal/auth/**"}}
	_, err = q2.Enqueue(ctx, api.WorkerTask{
		ParentSessionID: sess.ID,
		ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: "implementer",
		Prompt:    "write auth",
		Brief:     "fixture",
		Scope:     &writeScope,
		Status:    api.WorkerStatusPending,
	})
	testutil.FailErr(t, "enqueue write worker", err)

	gc = oar.NewGuardContext()
	testutil.FailErr(t, "Observe overlapping write", workeradmission.ObserveCoordinatorTaskInFlight(ctx, deps, sess, "task", map[string]any{
		"agent_type": "implementer",
		"brief":      map[string]any{"goal": "parallel write same area", "done_when": []any{"Return grounded results."}},
		"scope": map[string]any{
			"mode":  "write",
			"paths": []any{"internal/auth/x.go"},
		},
	}, gc))
	if len(gc.Invocation.ArgValidationErrors) > 0 {
		t.Fatalf("expected overlapping write scope allowed: %v", gc.Invocation.ArgValidationErrors)
	}
}

func TestWorkerPromptIncludesManifestTouchPaths(t *testing.T) {
	ctx := context.Background()
	reg, err := anchor.LoadRegistryFromConfigRoot()
	testutil.FailErr(t, "load anchor registry", err)
	// Restore the process-wide registry after the test.
	previous := anchor.DefaultRegistry()
	anchor.SetDefaultRegistry(reg)
	t.Cleanup(func() { anchor.SetDefaultRegistry(previous) })
	renderer := workerTestInjectRenderer(t)
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, "internal", "auth"), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	prompt, err := worker.BuildWorkerPrompt(ctx, renderer, "sess-inject-test", projectDir,
		[]string{"internal/**", "lycaon/**"},
		&api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"internal/auth/**"}},
		"Implement login fix",
	)
	if err != nil {
		t.Fatalf("BuildWorkerPrompt: %v", err)
	}
	if !strings.Contains(prompt, "Manifest touch paths") {
		t.Fatalf("prompt = %q", prompt)
	}
	if !strings.Contains(prompt, "internal/**") || !strings.Contains(prompt, "Task mode") {
		t.Fatalf("prompt = %q", prompt)
	}
	if strings.Contains(prompt, "lycaon/**") {
		t.Fatalf("lycaon touch path should be filtered: %q", prompt)
	}
	if !strings.HasSuffix(strings.TrimSpace(prompt), "Implement login fix") {
		t.Fatalf("prompt = %q", prompt)
	}
}
