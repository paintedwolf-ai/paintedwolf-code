package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

// A resume continues its child: the host supplies the agent, scope, planned
// leg, and ceiling, raised to the child's unanswered budget request.
func TestTaskToolResumeInheritsTheChild(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	var enqueued api.WorkerTask
	q := &priorJobQueue{
		WorkerQueue: worker.NewInMemoryQueue(2),
		prior: &api.WorkerTask{
			ID: "prior-job", ChildSessionID: "child-1", AgentType: "implementer",
			Scope:        &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"src/**"}},
			MaxToolLoops: 20,
			BudgetRequest: &api.WorkerBudgetRequest{
				Rounds: 12, RequestedMax: 32, RemainingWork: []string{"finish the parser"}, ToolLoopsUsed: 17,
			},
		},
		out: &enqueued,
	}
	testutil.FailErr(t, "register task tool", worker.RegisterTaskTool(reg, worker.TaskToolDeps{
		Queue:  q,
		Agents: orchestration.NewMemoryAgentRegistryForTest(), Workers: worker.DefaultWorkersConfig(),
	}))
	exec := toolexecution.NewExecutor(nil, reg, "coordinator")
	out, err := exec.Invoke(context.Background(), "task", map[string]any{
		"brief":            taskBrief("continue"),
		"child_session_id": "child-1",
	}, toolContext("parent-1", t.TempDir()))
	testutil.FailErr(t, "resume without restating the child", err)
	if enqueued.ChildSessionID != "child-1" || enqueued.AgentType != "implementer" {
		t.Fatalf("task = %+v", enqueued)
	}
	if !enqueued.EffectiveScope().IsWrite() || len(enqueued.EffectiveScope().Paths) != 1 {
		t.Fatalf("scope = %+v want the child's write scope", enqueued.Scope)
	}
	if enqueued.MaxToolLoops != 32 {
		t.Fatalf("max_tool_loops = %d want the unanswered request 32", enqueued.MaxToolLoops)
	}
	var payload map[string]any
	testutil.FailErr(t, "unmarshal JSON document", json.Unmarshal([]byte(out), &payload))
	if payload["child_session_id"] != "child-1" {
		t.Fatalf("payload = %v", payload)
	}
}

func TestTaskToolResumeRefusesToChangeTheChild(t *testing.T) {
	prior := &api.WorkerTask{
		ID: "prior-job", ChildSessionID: "child-1", AgentType: "security-reviewer",
		Scope: &api.TaskScope{Mode: api.TaskScopeModeRead},
	}
	for _, tc := range []struct {
		name  string
		args  map[string]any
		field string
	}{
		{"agent", map[string]any{"agent_type": "implementer"}, "agent_type"},
		{"scope", map[string]any{"scope": map[string]any{"mode": "write"}}, "scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := tools.NewDefaultRegistry()
			testutil.FailErr(t, "register task tool", worker.RegisterTaskTool(reg, worker.TaskToolDeps{
				Queue:  &priorJobQueue{WorkerQueue: worker.NewInMemoryQueue(2), prior: prior},
				Agents: orchestration.NewMemoryAgentRegistryForTest(), Workers: worker.DefaultWorkersConfig(),
			}))
			args := map[string]any{"brief": taskBrief("continue"), "child_session_id": "child-1"}
			for k, v := range tc.args {
				args[k] = v
			}
			_, err := reg.Run(t.Context(), "task", args, toolContext("parent-1", t.TempDir()))
			reject := toolrejection.AsToolReject(err)
			if reject == nil || reject.Code != "WORKER_RESUME_MISMATCH" || reject.Data["resume_field"] != tc.field {
				t.Fatalf("err = %v want WORKER_RESUME_MISMATCH on %s", err, tc.field)
			}
		})
	}
}

func TestTaskToolResumeOfUnknownChildRejects(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register task tool", worker.RegisterTaskTool(reg, worker.TaskToolDeps{
		Queue:  &priorJobQueue{WorkerQueue: worker.NewInMemoryQueue(2)},
		Agents: orchestration.NewMemoryAgentRegistryForTest(), Workers: worker.DefaultWorkersConfig(),
	}))
	_, err := reg.Run(t.Context(), "task", map[string]any{
		"agent_type": "implementer", "brief": taskBrief("continue"), "child_session_id": "missing",
	}, toolContext("parent-1", t.TempDir()))
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "WORKER_RESUME_CHILD_UNKNOWN" {
		t.Fatalf("err = %v want WORKER_RESUME_CHILD_UNKNOWN", err)
	}
}

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
				Scope: &api.TaskScope{Mode: api.TaskScopeModeRead, Paths: []string{"internal/**"}}, MaxToolLoops: 36,
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
	if enqueued.AgentType != "repo-researcher" || enqueued.MaxToolLoops != 36 || boundWorkID != "leg-2" {
		t.Fatalf("task = %+v bound=%q want the leg's agent and ceiling", enqueued, boundWorkID)
	}
	if paths := enqueued.EffectiveScope().Paths; len(paths) != 1 || paths[0] != "internal/**" {
		t.Fatalf("scope paths = %v want the leg's", paths)
	}
}

// A resumed child keeps its work identity only in the owning run and phase.
func TestTaskToolResumeKeepsItsWorkOnlyInItsPhase(t *testing.T) {
	for _, tc := range []struct {
		name, phase, priorWorkID, wantWorkID string
	}{
		{"same phase", "execute", "leg-1", "leg-1"},
		{"review question", "execute", "question/c6", "question/c6"},
		{"later phase", "challenge", "leg-1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := tools.NewDefaultRegistry()
			var boundWorkID string
			prior := &api.WorkerTask{
				ID: "prior-job", ChildSessionID: "child-1", AgentType: "repo-researcher",
				Scope: &api.TaskScope{Mode: api.TaskScopeModeRead}, WorkflowRunID: "run-1",
				WorkflowPhase: "execute", WorkflowWorkID: tc.priorWorkID, MaxToolLoops: 12,
			}
			testutil.FailErr(t, "register task tool", worker.RegisterTaskTool(reg, worker.TaskToolDeps{
				Queue:  &priorJobQueue{WorkerQueue: worker.NewInMemoryQueue(2), prior: prior},
				Agents: orchestration.NewMemoryAgentRegistryForTest(), Workers: worker.DefaultWorkersConfig(),
				WorkflowWork: func(context.Context, string, string) (spawn.WorkflowWork, bool, error) {
					return spawn.WorkflowWork{RunID: "run-1", Phase: tc.phase, AgentType: "repo-researcher"}, true, nil
				},
				BindWorkflowTask: func(_ context.Context, _ tools.ToolContext, workID string, _ *api.WorkerTask) error {
					boundWorkID = workID
					return nil
				},
			}))
			_, err := reg.Run(t.Context(), "task", map[string]any{
				"brief": taskBrief("continue"), "child_session_id": "child-1",
			}, toolContext("parent-1", t.TempDir()))
			testutil.FailErr(t, "resume", err)
			if boundWorkID != tc.wantWorkID {
				t.Fatalf("bound workflow_work_id = %q want %q", boundWorkID, tc.wantWorkID)
			}
		})
	}
}

func TestTaskToolOmittedBudgetIsTheHostDefault(t *testing.T) {
	budget := spawn.WorkerToolBudget{Default: 17, Min: 2, Max: 40}
	for _, tc := range []struct {
		name, agent, mode string
		want              int
	}{
		{name: "read", agent: "repo-researcher", mode: "read", want: 17},
		{name: "write", agent: "implementer", mode: "write", want: 17},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := tools.NewDefaultRegistry()
			var enqueued api.WorkerTask
			queue := &captureQueue{WorkerQueue: worker.NewInMemoryQueue(2), out: &enqueued}
			err := worker.RegisterTaskTool(reg, worker.TaskToolDeps{
				Queue:  queue,
				Agents: orchestration.NewMemoryAgentRegistryForTest(), Workers: worker.DefaultWorkersConfig(),
				ToolBudget: func(string) spawn.WorkerToolBudget { return budget },
			})
			testutil.FailErr(t, "register task tool", err)
			exec := toolexecution.NewExecutor(nil, reg, "coordinator")
			_, err = exec.Invoke(t.Context(), "task", map[string]any{
				"agent_type": tc.agent,
				"brief":      taskBrief("bounded work"),
				"scope":      map[string]any{"mode": tc.mode},
			}, toolContext("parent-1", t.TempDir()))
			testutil.FailErr(t, "invoke task tool", err)
			if enqueued.MaxToolLoops != tc.want {
				t.Fatalf("%s max_tool_loops = %d want %d", tc.mode, enqueued.MaxToolLoops, tc.want)
			}
		})
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

func TestTaskToolResumeRejectsPendingDecision(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	q := &priorJobQueue{
		WorkerQueue: worker.NewInMemoryQueue(2),
		prior: &api.WorkerTask{
			ID: "prior-job", ChildSessionID: "child-1", AgentType: "repo-researcher",
			Scope: &api.TaskScope{Mode: api.TaskScopeModeRead},
		},
	}
	if err := worker.RegisterTaskTool(reg, worker.TaskToolDeps{
		Queue:   q,
		Agents:  orchestration.NewMemoryAgentRegistryForTest(),
		Workers: worker.DefaultWorkersConfig(),
		PendingDecision: func(_ context.Context, childSessionID string) (string, bool, error) {
			if childSessionID == "child-1" {
				return "job-dec", true, nil
			}
			return "", false, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	exec := toolexecution.NewExecutor(nil, reg, "coordinator")
	_, err := exec.Invoke(context.Background(), "task", map[string]any{
		"agent_type":       "repo-researcher",
		"brief":            taskBrief("continue the same audit"),
		"child_session_id": "child-1",
		"scope":            map[string]any{"mode": "read", "paths": []any{"src/**"}},
	}, toolContext("parent-1", t.TempDir()))
	var reject *toolrejection.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "TASK_DECISION_PENDING" {
		t.Fatalf("err = %v want TASK_DECISION_PENDING reject", err)
	}
	if reject.Data["job_id"] != "job-dec" || reject.Data["child_session_id"] != "child-1" {
		t.Fatalf("reject data = %#v", reject.Data)
	}
}

func TestTaskToolResumeRejectsDiscardedOverlay(t *testing.T) {
	for _, status := range []api.WorkerMergeStatus{api.WorkerMergeStatusRejected, api.WorkerMergeStatusAborted} {
		t.Run(string(status), func(t *testing.T) {
			reg := tools.NewDefaultRegistry()
			q := &priorJobQueue{
				WorkerQueue: worker.NewInMemoryQueue(2),
				prior: &api.WorkerTask{
					ID: "prior-job", ChildSessionID: "child-1", MergeStatus: status, AgentType: "implementer",
					Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite},
				},
			}
			if err := worker.RegisterTaskTool(reg, worker.TaskToolDeps{
				Queue:   q,
				Agents:  orchestration.NewMemoryAgentRegistryForTest(),
				Workers: worker.DefaultWorkersConfig(),
			}); err != nil {
				t.Fatal(err)
			}
			exec := toolexecution.NewExecutor(nil, reg, "coordinator")
			_, err := exec.Invoke(context.Background(), "task", map[string]any{
				"agent_type":       "implementer",
				"brief":            taskBrief("fix it"),
				"child_session_id": "child-1",
				"scope":            map[string]any{"mode": "write", "paths": []any{"pkg"}},
			}, toolContext("parent-1", t.TempDir()))
			var reject *toolrejection.ToolReject
			if err == nil || !errors.As(err, &reject) || reject.Code != "WORKER_RESUME_OVERLAY_DISCARDED" {
				t.Fatalf("err = %v want WORKER_RESUME_OVERLAY_DISCARDED reject", err)
			}
		})
	}
}

func TestTaskToolResumePendingOverlayAllowed(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	var enqueued api.WorkerTask
	q := &priorJobQueue{
		WorkerQueue: worker.NewInMemoryQueue(2),
		prior: &api.WorkerTask{
			ID: "prior-job", ChildSessionID: "child-1", MergeStatus: api.WorkerMergeStatusPending, AgentType: "implementer",
			Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite},
		},
		out: &enqueued,
	}
	if err := worker.RegisterTaskTool(reg, worker.TaskToolDeps{
		Queue:   q,
		Agents:  orchestration.NewMemoryAgentRegistryForTest(),
		Workers: worker.DefaultWorkersConfig(),
	}); err != nil {
		t.Fatal(err)
	}
	exec := toolexecution.NewExecutor(nil, reg, "coordinator")
	_, err := exec.Invoke(context.Background(), "task", map[string]any{
		"agent_type":       "implementer",
		"brief":            taskBrief("continue the live overlay"),
		"child_session_id": "child-1",
		"scope":            map[string]any{"mode": "write", "paths": []any{"pkg"}},
	}, toolContext("parent-1", t.TempDir()))
	testutil.FailErr(t, "resume on pending overlay must enqueue", err)
	if enqueued.ChildSessionID != "child-1" {
		t.Fatalf("task = %+v", enqueued)
	}
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

func TestTaskToolDoesNotClampExplicitBudget(t *testing.T) {
	budget := spawn.WorkerToolBudget{Default: 17, Min: 2, Max: 40}
	for _, requested := range []int{1, 2, 40, 41} {
		reg := tools.NewDefaultRegistry()
		var enqueued api.WorkerTask
		queue := &captureQueue{WorkerQueue: worker.NewInMemoryQueue(2), out: &enqueued}
		err := worker.RegisterTaskTool(reg, worker.TaskToolDeps{Queue: queue, Agents: orchestration.NewMemoryAgentRegistryForTest(), Workers: worker.DefaultWorkersConfig(), ToolBudget: func(string) spawn.WorkerToolBudget { return budget }})
		testutil.FailErr(t, "register budgeted task", err)
		_, err = reg.Run(t.Context(), "task", map[string]any{"agent_type": "repo-researcher", "brief": taskBrief("bounded work"), "scope": map[string]any{"mode": "read"}, "max_tool_loops": requested}, toolContext("parent", t.TempDir()))
		if requested < budget.Min || requested > budget.Max {
			var reject *toolrejection.ToolReject
			if !errors.As(err, &reject) || reject.Code != workeradmission.TaskMaxToolLoopsInvalidCode || reject.Data["max_tool_loops"] != requested || reject.Data["host_max"] != budget.Max || enqueued.ID != "" {
				t.Fatalf("explicit budget changed or enqueued: requested=%d err=%v task=%+v", requested, err, enqueued)
			}
		} else {
			testutil.FailErr(t, "enqueue exact budget", err)
			if enqueued.MaxToolLoops != requested {
				t.Fatalf("budget=%d want %d", enqueued.MaxToolLoops, requested)
			}
		}
	}
}
