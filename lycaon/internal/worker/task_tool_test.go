package worker_test

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTaskToolWorkflowWorkSuppliesOmittedFields(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	var enqueued api.WorkerTask
	var boundWorkID string
	testutil.FailErr(t, "register task tool", worker.RegisterTaskTool(reg, worker.TaskToolDeps{
		Queue:  &captureQueue{WorkerQueue: worker.NewInMemoryQueue(2), out: &enqueued},
		Agents: orchestration.NewMemoryAgentRegistryForTest(), Workers: worker.DefaultWorkersConfig(),
		WorkflowWork: func(_ context.Context, _ string, workID string) (spawn.WorkflowWork, bool, error) {
			if workID != "leg-2" {
				return spawn.WorkflowWork{}, false, nil
			}
			return spawn.WorkflowWork{
				RunID: "run-1", Phase: "execute", AgentType: "repo-researcher",
				Scope: &api.TaskScope{Mode: api.TaskScopeModeRead, Paths: []string{"internal/**"}},
			}, true, nil
		},
		BindWorkflowTask: func(_ context.Context, _ tools.ToolContext, workID string, _ *api.WorkerTask) error {
			boundWorkID = workID
			return nil
		},
	}))
	_, err := reg.Run(t.Context(), "task", map[string]any{
		"brief": taskBrief("trace the subsystem"), "workflow_work_id": "leg-2",
	}, toolContext("parent-1", t.TempDir()))
	testutil.FailErr(t, "dispatch a planned leg by id", err)
	if enqueued.AgentType != "repo-researcher" || boundWorkID != "leg-2" {
		t.Fatalf("task = %+v bound=%q want the leg's agent", enqueued, boundWorkID)
	}
	if paths := enqueued.EffectiveScope().Paths; len(paths) != 1 || paths[0] != "internal/**" {
		t.Fatalf("scope paths = %v want the leg's", paths)
	}
}

// priorJobQueue supplies one resume target.
type priorJobQueue struct {
	worker.WorkerQueue
	prior *api.WorkerTask
	out   *api.WorkerTask
}

func (q *priorJobQueue) GetLatestByChildSessionID(context.Context, string) (*api.WorkerTask, bool) {
	if q.prior == nil {
		return nil, false
	}
	return q.prior, true
}

func (q *priorJobQueue) Enqueue(ctx context.Context, task api.WorkerTask) (string, error) {
	if q.out != nil {
		*q.out = task
	}
	return q.WorkerQueue.Enqueue(ctx, task)
}

func taskBrief(goal string) map[string]any {
	return map[string]any{"goal": goal, "done_when": []any{"Return grounded results."}}
}

type captureQueue struct {
	worker.WorkerQueue
	out *api.WorkerTask
}

func (c *captureQueue) Enqueue(ctx context.Context, task api.WorkerTask) (string, error) {
	*c.out = task
	return c.WorkerQueue.Enqueue(ctx, task)
}

func TestTaskToolDraftScratchSpawnsImplementerWithWorkspacePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	reg := tools.NewDefaultRegistry()
	var enqueued api.WorkerTask
	cq := &captureQueue{WorkerQueue: worker.NewInMemoryQueue(2), out: &enqueued}
	if err := worker.RegisterTaskTool(reg, worker.TaskToolDeps{
		Queue:   cq,
		Agents:  orchestration.NewMemoryAgentRegistryForTest(),
		Workers: worker.DefaultWorkersConfig(),
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	projReg := project.NewMemoryRegistry()
	p, err := projReg.Create(ctx, project.CreateParams{Draft: true})
	testutil.FailErr(t, "projReg.Create failed", err)
	scratch := project.PrimaryRootPath(p)
	roots := project.RootRefsFrom(p)
	if len(roots) != 1 {
		t.Fatalf("len(roots) = %d want 1", len(roots))
	}
	exec := toolexecution.NewExecutor(nil, reg, "coordinator")
	_, err = exec.Invoke(ctx, "task", map[string]any{
		"agent_type": "implementer",
		"brief":      taskBrief("ship core module"),
		"scope":      map[string]any{"mode": "write", "paths": []any{"src"}},
	}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "parent-draft",
		ProjectID: p.ID}, Source: tools.InvocationSource{Roots: roots,
		ActiveRootID: roots[0].ID},
	})
	testutil.FailErr(t, "task implementer on draft scratch", err)
	if enqueued.WorkspacePath != scratch {
		t.Fatalf("workspace_path = %q want %q", enqueued.WorkspacePath, scratch)
	}
	if enqueued.AgentType != "implementer" {
		t.Fatalf("agent_type = %q want implementer", enqueued.AgentType)
	}
}

func TestTaskToolFreshDispatchRequiresAgentType(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register task tool", worker.RegisterTaskTool(reg, worker.TaskToolDeps{
		Queue:   worker.NewInMemoryQueue(1),
		Agents:  orchestration.NewMemoryAgentRegistryForTest(),
		Workers: worker.DefaultWorkersConfig(),
	}))
	_, err := reg.Run(t.Context(), "task", map[string]any{
		"brief": taskBrief("new work"),
	}, toolContext("p", t.TempDir()))
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("err = %v want TOOL_ARGS_INVALID", err)
	}
	reason, _ := reject.Data["reason"].(string)
	if !strings.Contains(reason, "agent_type") {
		t.Fatalf("reason = %q want agent_type", reason)
	}
}

func TestTaskToolCapsAggregateBriefText(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	if err := worker.RegisterTaskTool(reg, worker.TaskToolDeps{
		Queue:  worker.NewInMemoryQueue(1),
		Agents: orchestration.NewMemoryAgentRegistryForTest(), Workers: worker.DefaultWorkersConfig(),
	}); err != nil {
		t.Fatal(err)
	}
	exec := toolexecution.NewExecutor(nil, reg, "coordinator")
	_, err := exec.Invoke(context.Background(), "task", map[string]any{
		"agent_type": "implementer",
		"brief": map[string]any{
			"goal":      strings.Repeat("x", spawn.MaxTaskCharterRunes),
			"done_when": []any{"done"},
		},
	}, toolContext("parent-1", t.TempDir()))
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "TOOL_ARGS_INVALID" || reject.Data["max_runes"] != spawn.MaxTaskCharterRunes {
		t.Fatalf("err = %v want aggregate brief cap reject", err)
	}
}

func toolContext(sessionID, dir string) tools.ToolContext {
	roots := []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}}
	return tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sessionID, ProjectID: testdbseed.DefaultProjectID}, Source: tools.InvocationSource{Roots: roots, ActiveRootID: "r1"}}
}

func TestTaskToolDispatchWriteScopeInsideLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "worktree")
	testutil.FailErr(t, "mkdir worktree/pkg", os.MkdirAll(filepath.Join(wt, "pkg"), 0o755))
	testutil.FailErr(t, "write worktree .git", os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /real/repo/.git/worktrees/wt\n"), 0o644))
	testutil.FailErr(t, "write worktree file", os.WriteFile(filepath.Join(wt, "pkg", "a.go"), []byte("package pkg\n"), 0o644))

	reg := tools.NewDefaultRegistry()
	var enqueued api.WorkerTask
	cq := &captureQueue{WorkerQueue: worker.NewInMemoryQueue(4), out: &enqueued}
	testutil.FailErr(t, "register task tool", worker.RegisterTaskTool(reg, worker.TaskToolDeps{
		Queue:   cq,
		Agents:  orchestration.NewMemoryAgentRegistryForTest(),
		Workers: worker.DefaultWorkersConfig(),
	}))

	rawReceipt, err := reg.Run(t.Context(), "task", map[string]any{
		"agent_type": "implementer",
		"brief":      taskBrief("update pkg/a.go in linked worktree"),
		"scope":      map[string]any{"mode": "write", "paths": []any{"worktree/pkg/a.go"}},
	}, toolContext("parent-wt", root))
	testutil.FailErr(t, "dispatch write leg in linked worktree", err)

	if !strings.Contains(enqueued.Prompt, "without VCS access") || !strings.Contains(enqueued.Prompt, "read-scoped leg or the coordinator") {
		t.Fatalf("expected worker orientation note in enqueued prompt, got:\n%s", enqueued.Prompt)
	}

	var receipt map[string]any
	testutil.FailErr(t, "decode dispatch receipt", json.Unmarshal([]byte(rawReceipt), &receipt))
	if receipt["linked_checkout"] != true || receipt["vcs_access"] != false {
		t.Fatalf("expected dispatch receipt to report linked_checkout=true and vcs_access=false, got: %#v", receipt)
	}
	note, _ := receipt["vcs_note"].(string)
	if !strings.Contains(note, "without VCS access") {
		t.Fatalf("expected dispatch receipt vcs_note, got: %#v", receipt)
	}

	// Read-scoped leg in the same linked worktree does not set the write-isolation fact.
	rawReadReceipt, err := reg.Run(t.Context(), "task", map[string]any{
		"agent_type": "repo-researcher",
		"brief":      taskBrief("inspect git history in linked worktree"),
		"scope":      map[string]any{"mode": "read", "paths": []any{"worktree/pkg/a.go"}},
	}, toolContext("parent-wt", root))
	testutil.FailErr(t, "dispatch read leg in linked worktree", err)
	var readReceipt map[string]any
	testutil.FailErr(t, "decode read dispatch receipt", json.Unmarshal([]byte(rawReadReceipt), &readReceipt))
	if _, has := readReceipt["linked_checkout"]; has {
		t.Fatalf("read leg receipt must not report write-only linked_checkout restriction: %#v", readReceipt)
	}
}
