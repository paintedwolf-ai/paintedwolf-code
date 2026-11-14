package worker_test

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
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
	var enqueued api.WorkerTask
	var boundWorkID string
	dir := t.TempDir()
	sqlDB := taskStore(t, dir)
	testdbseed.InsertWorkflowRun(t, sqlDB, "run-1", "parent-1", testdbseed.DefaultProjectID)
	reg := taskTool(t, sqlDB, capture(&enqueued), worker.TaskToolDeps{
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
	})
	_, err := reg.Run(t.Context(), "task", map[string]any{
		"brief": taskBrief("trace the subsystem"), "workflow_work_id": "leg-2",
	}, toolContext("parent-1", dir))
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

// taskStore is a store whose project has root r1 at dir and holds the
// given coordinator sessions.
func taskStore(t *testing.T, dir string, sessions ...string) *db.Store {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "task.db")
	testdbseed.InsertProjectRootWithID(t, sqlDB, testdbseed.DefaultProjectID, "r1", dir)
	for _, id := range sessions {
		testdbseed.InsertSession(t, sqlDB, id, testdbseed.DefaultProjectID)
	}
	return sqlDB
}

// taskTool registers the task tool over sqlDB's queue and worker records;
// wrap observes the queue.
func taskTool(t *testing.T, sqlDB *db.Store, wrap func(worker.WorkerQueue) worker.WorkerQueue, deps worker.TaskToolDeps) *tools.DefaultRegistry {
	t.Helper()
	var queue worker.WorkerQueue = worker.NewSQLQueue(sqlDB, 4)
	if wrap != nil {
		queue = wrap(queue)
	}
	deps.Queue, deps.WorkerRecords = queue, workerrecords.New(sqlDB)
	if deps.Agents == nil {
		deps.Agents = orchestration.NewMemoryAgentRegistryForTest()
	}
	deps.Workers = worker.DefaultWorkersConfig()
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register task tool", worker.RegisterTaskTool(reg, deps))
	return reg
}

func capture(out *api.WorkerTask) func(worker.WorkerQueue) worker.WorkerQueue {
	return func(q worker.WorkerQueue) worker.WorkerQueue { return &captureQueue{WorkerQueue: q, out: out} }
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
	var enqueued api.WorkerTask
	ctx := context.Background()
	projReg := project.NewMemoryRegistry()
	p, err := projReg.Create(ctx, project.CreateParams{Draft: true})
	testutil.FailErr(t, "projReg.Create failed", err)
	scratch := project.PrimaryRootPath(p)
	roots := project.RootRefsFrom(p)
	if len(roots) != 1 {
		t.Fatalf("len(roots) = %d want 1", len(roots))
	}
	sqlDB := taskStore(t, t.TempDir())
	testdbseed.InsertProjectRootWithID(t, sqlDB, p.ID, roots[0].ID, scratch)
	testdbseed.InsertSession(t, sqlDB, "parent-draft", p.ID)
	reg := taskTool(t, sqlDB, capture(&enqueued), worker.TaskToolDeps{})
	exec := toolexecution.NewExecutor(nil, reg, "coordinator")
	_, err = exec.Invoke(ctx, "task", map[string]any{
		"agent_type": "implementer",
		"brief":      taskBrief("ship core module"),
		"scope":      map[string]any{"mode": "write", "paths": []any{"src"}},
	}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "parent-draft",
		ProjectID: p.ID, ToolCallID: "call-draft"}, Source: tools.InvocationSource{Roots: roots,
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
	dir := t.TempDir()
	reg := taskTool(t, taskStore(t, dir, "p"), nil, worker.TaskToolDeps{})
	_, err := reg.Run(t.Context(), "task", map[string]any{
		"brief": taskBrief("new work"),
	}, toolContext("p", dir))
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
	dir := t.TempDir()
	reg := taskTool(t, taskStore(t, dir, "parent-1"), nil, worker.TaskToolDeps{})
	exec := toolexecution.NewExecutor(nil, reg, "coordinator")
	_, err := exec.Invoke(context.Background(), "task", map[string]any{
		"agent_type": "implementer",
		"brief": map[string]any{
			"goal":      strings.Repeat("x", spawn.MaxTaskCharterRunes),
			"done_when": []any{"done"},
		},
	}, toolContext("parent-1", dir))
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "TOOL_ARGS_INVALID" || reject.Data["max_runes"] != spawn.MaxTaskCharterRunes {
		t.Fatalf("err = %v want aggregate brief cap reject", err)
	}
}

var toolCalls atomic.Int64

// toolContext is one coordinator tool call in a project rooted at dir.
func toolContext(sessionID, dir string) tools.ToolContext {
	roots := []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}}
	call := "call-" + strconv.FormatInt(toolCalls.Add(1), 10)
	return tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sessionID, ProjectID: testdbseed.DefaultProjectID, ToolCallID: call}, Source: tools.InvocationSource{Roots: roots, ActiveRootID: "r1"}}
}

func TestTaskToolDispatchWriteScopeInsideLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "worktree")
	testutil.FailErr(t, "mkdir worktree/pkg", os.MkdirAll(filepath.Join(wt, "pkg"), 0o755))
	testutil.FailErr(t, "write worktree .git", os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /real/repo/.git/worktrees/wt\n"), 0o644))
	testutil.FailErr(t, "write worktree file", os.WriteFile(filepath.Join(wt, "pkg", "a.go"), []byte("package pkg\n"), 0o644))

	var enqueued api.WorkerTask
	reg := taskTool(t, taskStore(t, root, "parent-wt"), capture(&enqueued), worker.TaskToolDeps{})

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
