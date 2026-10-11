package session_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func observeTaskInFlight(t *testing.T, ctx context.Context, deps workeradmission.WorkerCycleGuardDeps, sess *api.Session, args map[string]any) *oar.GuardContext {
	t.Helper()
	gc := oar.NewGuardContext()
	testutil.FailErr(t, "workeradmission.ObserveCoordinatorTaskInFlight", workeradmission.ObserveCoordinatorTaskInFlight(ctx, deps, sess, "task", args, gc))
	return gc
}

func formatObserved(t *testing.T, fmttr *guidance.StaticRejectFormatter, gc *oar.GuardContext, code string) string {
	t.Helper()
	if fmttr == nil {
		return code
	}
	formatted, err := fmttr.Format(code, gc.RejectData[code])
	testutil.FailErr(t, "Format", err)
	return formatted
}

func TestObserveCoordinatorTaskReadRepoWideAllowed(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sess := &api.Session{
		ID:            "parent-1",
		ProjectID:     testdbseed.DefaultProjectID,
		WorkspacePath: dir,
		Posture:       api.SessionPostureBuild,
		AgentType:     "coordinator",
	}
	deps := workeradmission.WorkerCycleGuardDeps{
		Workers: worker.NewInMemoryQueue(8),
	}
	gc := observeTaskInFlight(t, ctx, deps, sess, map[string]any{
		"agent_type": "path-explorer",
		"brief":      testTaskBrief("repo-wide read scout"),
		"scope": map[string]any{
			"mode":  "read",
			"paths": []any{"**"},
		},
	})
	if evaluateHasCode(t, gc, workeradmission.TaskScopeWriteRequiredCode) {
		t.Fatal("expected read ** allowed")
	}
}

func TestObserveCoordinatorTaskMutationAgentRequiresWriteScope(t *testing.T) {
	ctx := context.Background()
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
	deps := workeradmission.WorkerCycleGuardDeps{
		Workers: worker.NewInMemoryQueue(8),
	}
	for _, agentType := range []string{"implementer", "plan-writer"} {
		capable, known := prompts.AgentMutationCapable(agentType)
		if !known || !capable {
			t.Fatalf("%s mutation capability = %v, known = %v", agentType, capable, known)
		}
		gc := observeTaskInFlight(t, ctx, deps, sess, map[string]any{
			"agent_type": agentType,
			"brief":      testTaskBrief("fix without write scope"),
		})
		if !evaluateHasCode(t, gc, workeradmission.TaskScopeWriteRequiredCode) {
			t.Fatalf("%s: expected TASK_SCOPE_WRITE_REQUIRED", agentType)
		}
		if !strings.Contains(formatObserved(t, guidance.NewStaticRejectFormatter(cfg), gc, workeradmission.TaskScopeWriteRequiredCode), workeradmission.TaskScopeWriteRequiredCode) {
			t.Fatalf("%s: format missing code", agentType)
		}
	}
	gc := observeTaskInFlight(t, ctx, deps, sess, map[string]any{
		"agent_type": "implementer",
		"brief":      testTaskBrief("explicit read scope"),
		"scope": map[string]any{
			"mode":  "read",
			"paths": []any{"internal/auth/**"},
		},
	})
	if !evaluateHasCode(t, gc, workeradmission.TaskScopeWriteRequiredCode) {
		t.Fatal("implementer read scope: expected TASK_SCOPE_WRITE_REQUIRED")
	}
}

func TestObserveCoordinatorTaskReadOnlyProfileRejectsWriteScope(t *testing.T) {
	ctx := context.Background()
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
	deps := workeradmission.WorkerCycleGuardDeps{
		Workers: worker.NewInMemoryQueue(8),
	}
	gc := observeTaskInFlight(t, ctx, deps, sess, map[string]any{
		"agent_type": "repo-researcher",
		"brief":      testTaskBrief("write report on readonly profile"),
		"scope": map[string]any{
			"mode":  "write",
			"paths": []any{"docs/report.md"},
		},
	})
	if !evaluateHasCode(t, gc, workeradmission.TaskScopeProfileReadOnlyCode) {
		t.Fatal("expected TASK_SCOPE_PROFILE_READ_ONLY")
	}
	if !strings.Contains(formatObserved(t, guidance.NewStaticRejectFormatter(cfg), gc, workeradmission.TaskScopeProfileReadOnlyCode), workeradmission.TaskScopeProfileReadOnlyCode) {
		t.Fatal("format missing TASK_SCOPE_PROFILE_READ_ONLY")
	}
}

func TestObserveCoordinatorTaskImplementerWriteScopeAllowed(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sess := &api.Session{
		ID:            "parent-1",
		ProjectID:     testdbseed.DefaultProjectID,
		WorkspacePath: dir,
		Posture:       api.SessionPostureBuild,
		AgentType:     "coordinator",
	}
	deps := workeradmission.WorkerCycleGuardDeps{
		Workers: worker.NewInMemoryQueue(8),
	}
	gc := observeTaskInFlight(t, ctx, deps, sess, map[string]any{
		"agent_type": "implementer",
		"brief":      testTaskBrief("update request handlers"),
		"scope": map[string]any{
			"mode":  "write",
			"paths": []any{"internal/http/**"},
		},
	})
	if evaluateHasCode(t, gc, workeradmission.TaskScopeWriteRequiredCode) || evaluateHasCode(t, gc, workeradmission.TaskScopeProfileReadOnlyCode) {
		t.Fatal("implementer write scope must be allowed")
	}
}

func TestObserveCoordinatorTaskWritePathsOptional(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sess := &api.Session{
		ID:            "parent-1",
		ProjectID:     testdbseed.DefaultProjectID,
		WorkspacePath: dir,
		Posture:       api.SessionPostureBuild,
		AgentType:     "coordinator",
	}
	deps := workeradmission.WorkerCycleGuardDeps{
		Workers: worker.NewInMemoryQueue(8),
	}
	gc := observeTaskInFlight(t, ctx, deps, sess, map[string]any{
		"agent_type": "implementer",
		"brief":      testTaskBrief("write without paths"),
		"scope": map[string]any{
			"mode": "write",
		},
	})
	if evaluateHasCode(t, gc, workeradmission.TaskScopeWriteRequiredCode) || evaluateHasCode(t, gc, workeradmission.TaskScopeProfileReadOnlyCode) {
		t.Fatal("write mode without suggested paths must be allowed")
	}
}

func TestObserveCoordinatorTaskRepoWideSuggestionAllowed(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sess := &api.Session{
		ID:            "parent-1",
		ProjectID:     testdbseed.DefaultProjectID,
		WorkspacePath: dir,
		Posture:       api.SessionPostureBuild,
		AgentType:     "coordinator",
	}
	deps := workeradmission.WorkerCycleGuardDeps{
		Workers: worker.NewInMemoryQueue(8),
	}
	gc := observeTaskInFlight(t, ctx, deps, sess, map[string]any{
		"agent_type": "implementer",
		"brief":      testTaskBrief("too broad"),
		"scope": map[string]any{
			"mode":  "write",
			"paths": []any{"**/*"},
		},
	})
	if evaluateHasCode(t, gc, workeradmission.TaskScopeWriteRequiredCode) || evaluateHasCode(t, gc, workeradmission.TaskScopeProfileReadOnlyCode) {
		t.Fatal("repo-wide suggested paths must be allowed")
	}
}

func TestParallelWriteWorkersNonOverlappingAllowed(t *testing.T) {
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
	deps := workeradmission.WorkerCycleGuardDeps{
		Workers: q,
	}
	_, err := q.Enqueue(ctx, api.WorkerTask{
		ParentSessionID: sess.ID,
		ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: "implementer",
		Prompt:    "write auth",
		Brief:     "fixture",
		Scope:     &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"internal/auth/login.go"}},
	})
	testutil.FailErr(t, "Enqueue", err)

	gc := observeTaskInFlight(t, ctx, deps, sess, map[string]any{
		"agent_type": "implementer",
		"brief":      testTaskBrief("parallel write other area"),
		"scope": map[string]any{
			"mode":  "write",
			"paths": []any{"internal/billing/invoice.go"},
		},
	})
	if evaluateHasCode(t, gc, workeradmission.CoordinatorWorkerInFlightCode) {
		t.Fatal("expected parallel non-overlapping write allowed")
	}
}

func TestParallelWriteWorkersOverlappingAllowed(t *testing.T) {
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
	deps := workeradmission.WorkerCycleGuardDeps{
		Workers: q,
	}
	_, err := q.Enqueue(ctx, api.WorkerTask{
		ParentSessionID: sess.ID,
		ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: "implementer",
		Prompt:    "write parser",
		Brief:     "fixture",
		Scope:     &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"shellsim/parser.py"}},
	})
	testutil.FailErr(t, "Enqueue", err)

	gc := observeTaskInFlight(t, ctx, deps, sess, map[string]any{
		"agent_type": "implementer",
		"brief":      testTaskBrief("parallel write overlapping scope"),
		"scope": map[string]any{
			"mode":  "write",
			"paths": []any{"shellsim/parser.py"},
		},
	})
	if evaluateHasCode(t, gc, workeradmission.CoordinatorWorkerInFlightCode) {
		t.Fatal("expected overlapping write allowed (no spawn-time reject)")
	}
}

func TestParallelReadScoutsAllowedUnderReadCap(t *testing.T) {
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
	deps := workeradmission.WorkerCycleGuardDeps{
		Workers:         q,
		MaxWorkers:      func(context.Context, string) int { return 3 },
		MaxReadWorkers:  func(context.Context, string) int { return 3 },
		MaxWriteWorkers: func(context.Context, string) int { return 1 },
	}
	readScope := api.TaskScope{Mode: api.TaskScopeModeRead, Paths: []string{"internal/a/**"}}
	readScoutArgs := map[string]any{
		"agent_type": "path-explorer",
		"brief":      testTaskBrief("read scout"),
		"scope":      map[string]any{"mode": "read", "paths": []any{"internal/a/**"}},
	}
	for i := 0; i < 3; i++ {
		gc := observeTaskInFlight(t, ctx, deps, sess, readScoutArgs)
		if evaluateHasCode(t, gc, workeradmission.CoordinatorWorkerInFlightCode) || gc.Workers.WorkerSpawnBlocked {
			t.Fatalf("expected read scout %d allowed", i+1)
		}
		_, err := q.Enqueue(ctx, api.WorkerTask{
			ParentSessionID: sess.ID,
			ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
			AgentType: "path-explorer",
			Prompt:    "scout",
			Brief:     "fixture",
			Scope:     &readScope,
		})
		testutil.FailErr(t, "Enqueue", err)
	}
	gc := observeTaskInFlight(t, ctx, deps, sess, readScoutArgs)
	if !evaluateHasCode(t, gc, workeradmission.CoordinatorWorkerInFlightCode) || !gc.Workers.WorkerSpawnBlocked {
		t.Fatal("expected reject at read cap")
	}
	if gc.Workers.MaxReadWorkers != 3 || gc.Workers.ActiveReadCount != 3 {
		t.Fatalf("read cap facts = %d/%d, want 3/3", gc.Workers.ActiveReadCount, gc.Workers.MaxReadWorkers)
	}
}

func TestObserveCoordinatorTaskRejectsExploreReadonlyOnEmptyRepo(t *testing.T) {
	ctx := context.Background()
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
	deps := workeradmission.WorkerCycleGuardDeps{
		Workers: worker.NewInMemoryQueue(8),
		RepoKnownEmpty: func(context.Context, string) bool {
			return true
		},
	}

	// Sample agents that resolve to !mutation + project-tree survey (different tool
	// profile ids): the gate follows surface tools, not an agent-id allowlist.
	for _, agentType := range []string{"path-explorer", "plan-reviewer"} {
		surveys, ok := prompts.AgentSurveysProjectTree(agentType)
		if !ok || !surveys {
			t.Fatalf("%s should survey the project tree", agentType)
		}
		capable, ok := prompts.AgentMutationCapable(agentType)
		if !ok || capable {
			t.Fatalf("%s should not be mutation-capable", agentType)
		}
		gc := observeTaskInFlight(t, ctx, deps, sess, map[string]any{
			"agent_type": agentType,
			"brief":      testTaskBrief("survey empty tree"),
			"scope": map[string]any{
				"mode":  "read",
				"paths": []any{"**"},
			},
		})
		if !evaluateHasCode(t, gc, workeradmission.RepoEmptyReadOnlyWorkerCode) {
			t.Fatalf("%s: expected %s", agentType, workeradmission.RepoEmptyReadOnlyWorkerCode)
		}
		if !strings.Contains(formatObserved(t, guidance.NewStaticRejectFormatter(cfg), gc, workeradmission.RepoEmptyReadOnlyWorkerCode), workeradmission.RepoEmptyReadOnlyWorkerCode) {
			t.Fatalf("%s: format missing %s", agentType, workeradmission.RepoEmptyReadOnlyWorkerCode)
		}
	}
	gc := observeTaskInFlight(t, ctx, deps, sess, map[string]any{
		"agent_type": "implementer",
		"brief":      testTaskBrief("bootstrap empty tree"),
		"scope": map[string]any{
			"mode":  "write",
			"paths": []any{"README.md"},
		},
	})
	if evaluateHasCode(t, gc, workeradmission.RepoEmptyReadOnlyWorkerCode) {
		t.Fatal("implementer must remain allowed on empty repo")
	}
	gc = observeTaskInFlight(t, ctx, deps, sess, map[string]any{
		"agent_type": "web-researcher",
		"brief":      testTaskBrief("external docs"),
		"scope": map[string]any{
			"mode": "read",
		},
	})
	if evaluateHasCode(t, gc, workeradmission.RepoEmptyReadOnlyWorkerCode) {
		t.Fatal("web-researcher must remain allowed on empty repo")
	}
	surveys, ok := prompts.AgentSurveysProjectTree("web-researcher")
	if !ok {
		t.Fatal("web-researcher profile must resolve")
	}
	if surveys {
		t.Fatal("web-researcher must not count as project-tree survey")
	}
}

func TestObserveCoordinatorTaskAllowsMissingSuggestedPath(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sess := &api.Session{
		ID:            "parent-missing-scope",
		ProjectID:     testdbseed.DefaultProjectID,
		WorkspacePath: dir,
		Posture:       api.SessionPostureBuild,
		AgentType:     "coordinator",
	}
	deps := workeradmission.WorkerCycleGuardDeps{Workers: worker.NewInMemoryQueue(8)}
	gc := observeTaskInFlight(t, ctx, deps, sess, map[string]any{
		"agent_type": "path-explorer",
		"brief":      testTaskBrief("survey the providers"),
		"scope": map[string]any{
			"mode":  "read",
			"paths": []any{"src/coropa/mcp", "src/coropa/providers"},
		},
	})
	if evaluateHasCode(t, gc, workeradmission.TaskScopeWriteRequiredCode) || evaluateHasCode(t, gc, workeradmission.TaskScopeProfileReadOnlyCode) {
		t.Fatal("missing suggested paths must not block dispatch")
	}
}
