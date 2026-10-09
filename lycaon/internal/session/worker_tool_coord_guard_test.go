package session_test

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"testing"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBeforeWorkerWriteRejectsReadScopedMutation(t *testing.T) {
	mgr := session.NewManager(nil, nil, nil, settings.SessionLimits{})
	q := worker.NewInMemoryQueue(8)
	mgr.SetWorkerQueue(q)
	dir := t.TempDir()
	readScope := api.TaskScope{Mode: api.TaskScopeModeRead, Paths: []string{"internal/**"}}
	_, err := q.Enqueue(context.Background(), api.WorkerTask{
		ID:              "job-read",
		ParentSessionID: "parent",
		ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: "implementer",
		Prompt:    "read scout mis-dispatch",
		Brief:     "fixture",
		Scope:     &readScope,
		Status:    api.WorkerStatusRunning,
	})
	testutil.FailErr(t, "enqueue read worker", err)
	tctx := tools.ToolContext{
		WorkerJobID:   "job-read",
		TurnSurfaceID: "implement_dispatch",
	}
	err = mgr.BeforeWorkerWrite(context.Background(), tctx, "src/foo.go")
	if err == nil {
		t.Fatal("expected read-scoped mutation reject")
	}
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) {
		t.Fatalf("expected ToolReject: %T %v", err, err)
	}
	if reject.Code != session.TaskScopeReadMutationDeniedCode {
		t.Fatalf("code = %q want %q", reject.Code, session.TaskScopeReadMutationDeniedCode)
	}
}

func TestBeforeWorkerWriteAllowsPathOutsideSuggestion(t *testing.T) {
	mgr := session.NewManager(nil, nil, nil, settings.SessionLimits{})
	q := worker.NewInMemoryQueue(8)
	mgr.SetWorkerQueue(q)
	writeScope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"src/allowed/**"}}
	_, err := q.Enqueue(context.Background(), api.WorkerTask{
		ID: "job-write", ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: t.TempDir(), AgentType: "implementer", Prompt: "scoped write", Brief: "fixture",
		Scope: &writeScope, Status: api.WorkerStatusRunning,
	})
	testutil.FailErr(t, "enqueue write worker", err)
	err = mgr.BeforeWorkerWrite(context.Background(), tools.ToolContext{WorkerJobID: "job-write"}, "src/outside.go")
	testutil.FailErr(t, "write outside suggested path", err)
}

func TestEnsureWorkerBranchLeavesReadScopedWorkerAttached(t *testing.T) {
	mgr, ctx := readScopedWorkerManager(t, "job-read-branch", api.TaskScopeModeRead)
	tctx, err := mgr.EnsureWorkerBranch(ctx, tools.ToolContext{WorkerJobID: "job-read-branch"})
	testutil.FailErr(t, "ensure read worker branch", err)
	if tctx.WorkerBranchRoot != "" {
		t.Fatalf("WorkerBranchRoot = %q, want source-attached", tctx.WorkerBranchRoot)
	}
	if tctx.BranchWorkspace != nil {
		t.Fatal("read worker must not carry a branch workspace")
	}
	if tctx.SourceWorkspaceKind != api.SourceWorkspaceKindProject {
		t.Fatalf("workspace kind = %q, want project: a read worker reads the project tree and its open documents", tctx.SourceWorkspaceKind)
	}
}

func TestEnsureWorkerBranchFailsWriteScopeWithoutWorkspace(t *testing.T) {
	mgr, ctx := readScopedWorkerManager(t, "job-write-branch", api.TaskScopeModeWrite)
	if _, err := mgr.EnsureWorkerBranch(ctx, tools.ToolContext{WorkerJobID: "job-write-branch"}); err == nil {
		t.Fatal("expected claim failure for a write worker with no workspace manager")
	}
}

func TestWorkerReadToolsIgnoreSuggestedPaths(t *testing.T) {
	ctx := context.Background()
	mgr := session.NewManager(nil, nil, nil, settings.SessionLimits{})
	queue := worker.NewInMemoryQueue(8)
	mgr.SetWorkerQueue(queue)
	scope := api.TaskScope{Mode: api.TaskScopeModeRead, Paths: []string{"docs/README.md"}}
	child := api.Session{ID: "child-read-discovery", ParentSessionID: "parent"}
	_, err := queue.Enqueue(ctx, api.WorkerTask{
		ID: "job-read-discovery", ParentSessionID: child.ParentSessionID,
		ChildSessionID: child.ID, ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: t.TempDir(), AgentType: "repo-researcher",
		Prompt: "inspect documentation", Brief: "fixture", Scope: &scope,
		Status: api.WorkerStatusRunning,
	})
	testutil.FailErr(t, "enqueue discovery worker", err)

	tctx, err := mgr.EnrichWorkerToolContext(ctx, &child, tools.ToolContext{WorkerJobID: "job-read-discovery"})
	testutil.FailErr(t, "enrich worker context", err)
	registry := tools.NewDefaultRegistry()
	for _, name := range []string{"list_dir", "read"} {
		err := registry.Register(name, func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			return "ok", nil
		})
		testutil.FailErr(t, "register "+name, err)
	}
	executor := toolexecution.NewExecutor(nil, registry, "implement")
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{name: "list_dir", args: map[string]any{"path": "."}},
		{name: "read", args: map[string]any{"path": "lycaon/internal/toolexecution/executor_impl.go"}},
	} {
		out, invokeErr := executor.Invoke(ctx, call.name, call.args, tctx)
		testutil.FailErr(t, "invoke "+call.name, invokeErr)
		if out != "ok" {
			t.Fatalf("%s output = %q, want ok", call.name, out)
		}
	}
}

func readScopedWorkerManager(t *testing.T, jobID string, mode api.TaskScopeMode) (*session.Manager, context.Context) {
	t.Helper()
	mgr := session.NewManager(nil, nil, nil, settings.SessionLimits{})
	q := worker.NewInMemoryQueue(8)
	mgr.SetWorkerQueue(q)
	scope := api.TaskScope{Mode: mode, Paths: []string{"."}}
	ctx := context.Background()
	_, err := q.Enqueue(ctx, api.WorkerTask{
		ID:              jobID,
		ParentSessionID: "parent",
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspacePath:   t.TempDir(),
		AgentType:       "web-researcher",
		Prompt:          "fetch release notes",
		Brief:           "fixture",
		Scope:           &scope,
		Status:          api.WorkerStatusRunning,
	})
	testutil.FailErr(t, "enqueue branch worker", err)
	return mgr, ctx
}
