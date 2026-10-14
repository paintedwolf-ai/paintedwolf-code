package wiring

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerOutcomesAdvancePlannedWorkflow(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		t.Run(fmt.Sprintf("acknowledged=%t", recovered), func(t *testing.T) {
			h := BuildForTest(t, WithoutCoordinatorLoop())
			ctx := t.Context()
			dir := h.ProjectDir(t, "survey")
			sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureVet}, dir)
			testutil.FailErr(t, "create survey", err)
			run, err := h.Workflows.Manager.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{WorkflowID: "security-survey", WorkflowVersion: "2.0.0"})
			testutil.FailErr(t, "start survey", err)
			settleScanObligationAndAdvance(t, h, ctx, run.ID, "plan")
			call := wiringToolContext(sess.ID, dir)
			call.Identity.Agent = "coordinator"
			call.Identity.ProjectID = sess.ProjectID
			testutil.FailErr(t, "resolve project root", h.DB.QueryRowContext(ctx, "SELECT id FROM project_roots WHERE project_id = ? AND is_primary = 1", sess.ProjectID).Scan(&call.Source.ActiveRootID))
			call.Source.Roots[0].ID = call.Source.ActiveRootID
			call.Identity.ToolCallID = uuid.NewString()
			legs := []any{}
			for i := 1; i <= 5; i++ {
				legs = append(legs, map[string]any{"agent_type": "security-reviewer", "subject": fmt.Sprintf("Boundary %d", i), "prompt": "Trace the assigned boundary and account for unexamined scope.", "done_when": []any{"Account for the assigned boundary"}})
			}
			output, err := h.ToolRegistry.Run(ctx, "fanout_plan", map[string]any{"legs": legs, "threat_model": "Local desktop application with untrusted project content and confined subprocesses."}, call)
			testutil.FailErr(t, "plan survey", err)
			if output == "" {
				t.Fatal("plan returned no result")
			}
			_, err = h.Workflows.Manager.Phases.Advance(ctx, run.ID)
			testutil.FailErr(t, "enter execution", err)
			for i := 1; i <= 5; i++ {
				call.Identity.ToolCallID = uuid.NewString()
				args := map[string]any{"workflow_work_id": fmt.Sprintf("leg-%d", i)}
				_, blocked, err := h.Sessions.Manager.Coordinator.Guards.BeforeTool(ctx, sess, nil, "", "task", args)
				testutil.FailErr(t, "admit planned identity", err)
				if blocked {
					t.Fatal("planned task admission blocked")
				}
				_, err = h.ToolRegistry.Run(ctx, "task", args, call)
				testutil.FailErr(t, "dispatch survey worker", err)
			}
			tasks, err := h.Delegations.Queue.ListByWorkflowRunID(ctx, run.ID)
			testutil.FailErr(t, "load dispatched workers", err)
			if len(tasks) != 5 {
				t.Fatalf("workers=%d, want five", len(tasks))
			}
			for _, task := range tasks {
				if task.AgentType != "security-reviewer" || task.EffectiveScope().Mode != api.TaskScopeModeRead {
					t.Fatalf("planned identity = %+v", task)
				}
			}
			changed := tasks[0]
			changed.ID, changed.SourceToolCallID, changed.AgentType = uuid.NewString(), uuid.NewString(), "implementer"
			reject := toolrejection.AsToolReject(h.Delegations.Queue.PrepareEnqueue(ctx, sess.ProjectID, &changed))
			if reject == nil || reject.Data["reason"] != "planned_agent_mismatch" {
				t.Fatalf("queue accepted changed planned agent: %v", reject)
			}
			for range tasks {
				task, err := h.Delegations.Queue.ClaimNext(ctx, worker.ClaimRequest{ProjectID: sess.ProjectID, ClaimedBy: "fixture", ExecutionTarget: api.ExecutionTargetLocal})
				testutil.FailErr(t, "claim worker", err)
				if task == nil {
					t.Fatal("worker was not claimable")
				}
				child, err := h.Store.CreateChild(ctx, sess, api.SpawnChildRequest{AgentType: task.AgentType, Prompt: task.Prompt})
				testutil.FailErr(t, "create worker session", err)
				testutil.FailErr(t, "bind worker session", h.Delegations.Queue.SetChildSessionID(ctx, task.ID, child.ID))
				won, err := h.Delegations.Queue.Complete(ctx, task, api.WorkerResult{Status: "complete", Summary: "Assigned boundary reviewed.", Grounding: &api.CitationGrounding{}, CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete", Brief: "Assigned boundary reviewed."}})
				testutil.FailErr(t, "commit worker result", err)
				if !won {
					t.Fatal("worker claim was lost")
				}
			}
			if recovered {
				// Recreate acknowledged delivery with the production summary projector.
				for _, original := range tasks {
					task, _ := h.Delegations.Queue.Get(original.ID)
					input, ok := h.Sessions.Manager.Workers.Results.TaskByID(task.ID)
					if !ok {
						t.Fatal("worker identity missing")
					}
					_, err := h.Sessions.Manager.Workers.Results.ProjectResult(ctx, input, *task.Result)
					testutil.FailErr(t, "project retained result", err)
					testutil.FailErr(t, "acknowledge retained result", h.Delegations.Queue.MarkOutcomeDelivered(ctx, task.ID))
				}
				testutil.FailErr(t, "reconcile before coordinator request", h.Sessions.Manager.Coordinator.Control.AssertRunnable(ctx, sess.ID))
			} else {
				stop := h.StartBackgroundWorkers(t, ctx)
				t.Cleanup(stop)
			}
			waitWorkflowPhase(t, ctx, h.Workflows.Manager, run.ID, "claims")
			stale := tasks[0]
			stale.ID, stale.SourceToolCallID = uuid.NewString(), uuid.NewString()
			reject = toolrejection.AsToolReject(h.Delegations.Queue.PrepareEnqueue(ctx, sess.ProjectID, &stale))
			if reject == nil || reject.Data["reason"] != "workflow_phase_changed" {
				t.Fatalf("queue accepted stale workflow phase: %v", reject)
			}
			testutil.WaitFor(t, 5*time.Second, func() bool {
				pending, err := h.Delegations.Queue.ListPendingOutcomes(ctx)
				return err == nil && len(pending) == 0
			})
			summaries, err := h.Sessions.Manager.Workers.Summaries.Tags(ctx, sess.ID)
			testutil.FailErr(t, "load worker summaries", err)
			if len(summaries) != 5 {
				t.Fatalf("summaries=%d, want five", len(summaries))
			}
			before, err := h.Workflows.Manager.Store.Runs.Get(ctx, run.ID)
			testutil.FailErr(t, "read reconciled run", err)
			testutil.FailErr(t, "repeat reconciliation", h.Sessions.Manager.Coordinator.Control.AssertRunnable(ctx, sess.ID))
			after, err := h.Workflows.Manager.Store.Runs.Get(ctx, run.ID)
			testutil.FailErr(t, "read repeated reconciliation", err)
			if after.Revision != before.Revision {
				t.Fatal("reconciliation repeated a settled transition")
			}
			tasks, err = h.Delegations.Queue.ListByWorkflowRunID(ctx, run.ID)
			testutil.FailErr(t, "read retained work", err)
			if len(tasks) != 5 {
				t.Fatal("completion created duplicate work")
			}
		})
	}
}
