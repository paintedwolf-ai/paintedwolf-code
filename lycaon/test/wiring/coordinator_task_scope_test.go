package wiring

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"reflect"
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

	q := h.Delegations.Queue
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
		if _, rejected := gc.RejectData[workeradmission.CoordinatorWorkerInFlightCode]; rejected || gc.Workers.WorkerSpawnBlocked {
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
	testutil.FailErr(t, "Observe overlapping write", workeradmission.ObserveCoordinatorTaskInFlight(ctx, deps, sess, "task", resolveObservedTask(t, map[string]any{
		"agent_type": "implementer",
		"brief":      map[string]any{"goal": "parallel write same area", "done_when": []any{"Return grounded results."}},
		"scope": map[string]any{
			"mode":  "write",
			"paths": []any{"internal/auth/x.go"},
		},
	}), gc))
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

func resolveObservedTask(t *testing.T, args map[string]any) workeradmission.TaskIdentity {
	t.Helper()
	identity, err := (workeradmission.TaskIdentityResolver{}).Resolve(t.Context(), "parent-1", "", args)
	testutil.FailErr(t, "resolve observed task", err)
	return identity
}

func TestTaskAdmissionUsesTheExecutedIdentity(t *testing.T) {
	h := BuildForTest(t, WithoutCoordinatorLoop())
	for _, tc := range []struct{ name, agent, mode, omitted string }{
		{"explicit read", "repo-researcher", "read", ""},
		{"explicit write", "implementer", "write", ""},
		{"resume read", "repo-researcher", "read", "both"},
		{"resume write", "implementer", "write", "both"},
		{"resume omitted agent", "implementer", "write", "agent"},
		{"resume omitted scope", "implementer", "write", "scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			dir := h.ProjectDir(t, tc.name)
			testutil.FailErr(t, "seed project dir", os.MkdirAll(filepath.Join(dir, "src"), 0o755))
			testutil.FailErr(t, "seed project source", os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main\n"), 0o644))
			sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, dir)
			testutil.FailErr(t, "create coordinator", err)
			call := wiringToolContext(sess.ID, dir)
			call.Identity.Agent, call.Identity.ProjectID, call.Identity.ToolCallID = "coordinator", sess.ProjectID, uuid.NewString()
			testutil.FailErr(t, "resolve project root", h.DB.QueryRowContext(ctx, "SELECT id FROM project_roots WHERE project_id = ? AND path = ?", sess.ProjectID, dir).Scan(&call.Source.ActiveRootID))
			call.Source.Roots[0].ID = call.Source.ActiveRootID
			sess.WorkspaceRootID = call.Source.ActiveRootID
			_, err = h.ToolRegistry.Run(ctx, "update_progress", map[string]any{"content": "## Progress\n- [ ] Complete assigned work"}, call)
			testutil.FailErr(t, "record progress", err)
			scope := api.TaskScope{Mode: api.TaskScopeMode(tc.mode), Paths: []string{"src/**"}}
			args := map[string]any{"agent_type": tc.agent, "scope": map[string]any{"mode": tc.mode, "paths": []any{"src/**"}}, "brief": map[string]any{"goal": "Continue assigned work", "done_when": []any{"Return grounded results"}}}
			if tc.omitted != "" {
				child, err := h.Store.CreateChild(ctx, sess, api.SpawnChildRequest{AgentType: tc.agent, Prompt: "Assigned work"})
				testutil.FailErr(t, "create recorded child", err)
				_, err = h.Delegations.Queue.Enqueue(ctx, api.WorkerTask{ParentSessionID: sess.ID, ChildSessionID: child.ID, ProjectID: sess.ProjectID, WorkspaceRootID: call.Source.ActiveRootID, WorkspacePath: dir, AgentType: tc.agent, Scope: &scope, Prompt: "Assigned work", Brief: "Assigned work", Status: api.WorkerStatusComplete})
				testutil.FailErr(t, "record prior job", err)
				args["child_session_id"] = child.ID
				if tc.omitted == "both" || tc.omitted == "agent" {
					delete(args, "agent_type")
				}
				if tc.omitted == "both" || tc.omitted == "scope" {
					delete(args, "scope")
				}
			}
			before, err := json.Marshal(args)
			testutil.FailErr(t, "capture original arguments", err)
			_, blocked, err := h.Sessions.Manager.Coordinator.Guards.BeforeTool(ctx, sess, nil, "", "task", args)
			testutil.FailErr(t, "admit effective identity", err)
			if blocked {
				t.Fatal("task admission blocked")
			}
			call.Identity.ToolCallID = uuid.NewString()
			_, err = h.ToolRegistry.Run(ctx, "task", args, call)
			testutil.FailErr(t, "execute admitted task", err)
			task, err := h.Delegations.Queue.TaskReceipt(ctx, sess.ID, call.Identity.ToolCallID)
			testutil.FailErr(t, "read task receipt", err)
			if task == nil || task.AgentType != tc.agent || !reflect.DeepEqual(task.EffectiveScope(), scope) {
				t.Fatalf("executed identity = %+v, want %s %+v", task, tc.agent, scope)
			}
			after, err := json.Marshal(args)
			testutil.FailErr(t, "capture retained arguments", err)
			if string(before) != string(after) {
				t.Fatal("admission changed original tool arguments")
			}
		})
	}
}

func TestTaskAdmissionPreservesTypedResolutionFailure(t *testing.T) {
	h := BuildForTest(t, WithoutCoordinatorLoop())
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, h.ProjectDir(t, "invalid-identity"))
	testutil.FailErr(t, "create coordinator", err)
	child, err := h.Store.CreateChild(t.Context(), sess, api.SpawnChildRequest{AgentType: "repo-researcher", Prompt: "Assigned work"})
	testutil.FailErr(t, "create child for mismatch", err)
	_, err = h.Delegations.Queue.Enqueue(t.Context(), api.WorkerTask{ParentSessionID: sess.ID, ChildSessionID: child.ID, ProjectID: sess.ProjectID, WorkspaceRootID: sess.WorkspaceRootID, WorkspacePath: sess.WorkspacePath, AgentType: "repo-researcher", Scope: &api.TaskScope{Mode: api.TaskScopeModeRead}, Prompt: "Assigned work", Brief: "Assigned work", Status: api.WorkerStatusComplete})
	testutil.FailErr(t, "record child identity", err)
	for _, tc := range []struct {
		name string
		args map[string]any
		code string
	}{
		{"unknown profile", map[string]any{"agent_type": "missing-worker", "scope": map[string]any{"mode": "write"}}, "WORKER_TYPE_UNAVAILABLE"},
		{"unknown child", map[string]any{"child_session_id": uuid.NewString()}, "WORKER_RESUME_CHILD_UNKNOWN"},
		{"missing identity", map[string]any{}, "TOOL_ARGS_INVALID"},
		{"agent mismatch", map[string]any{"child_session_id": child.ID, "agent_type": "implementer"}, "WORKER_RESUME_MISMATCH"},
		{"scope mismatch", map[string]any{"child_session_id": child.ID, "scope": map[string]any{"mode": "write"}}, "WORKER_RESUME_MISMATCH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := h.Sessions.Manager.Coordinator.Guards.BeforeTool(t.Context(), sess, nil, "", "task", tc.args)
			reject := toolrejection.AsToolReject(err)
			if reject == nil || reject.Code != tc.code {
				t.Fatalf("rejection = %v, want typed %s", err, tc.code)
			}
			refusal, ok := guidance.RefusalFromError(err)
			if !ok || refusal.Code() != tc.code || !strings.Contains(refusal.Error(), "Code: "+tc.code) {
				t.Fatalf("resolution failure lost OAR recovery: %v", err)
			}
		})
	}
}
