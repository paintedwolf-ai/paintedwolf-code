package worker

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

type workflowAdmissionProbe struct {
	runErr, taskErr     error
	runCalls, taskCalls int
	task                api.WorkerTask
}

func (p *workflowAdmissionProbe) AssertRunnable(context.Context, string) error {
	p.runCalls++
	return p.runErr
}

func (p *workflowAdmissionProbe) AssertWorkerTask(_ context.Context, task *api.WorkerTask) error {
	p.taskCalls++
	p.task = *task
	return p.taskErr
}

func TestQueueWorkflowAdmissionContract(t *testing.T) {
	rejected := errors.New("workflow admission rejected")
	for _, backend := range []string{"memory", "sql"} {
		for _, tc := range []struct {
			name, run, phase        string
			runErr, taskErr         error
			wantRun, wantTask       int
			missingRun, missingTask bool
		}{
			{name: "unbound"},
			{name: "run only", run: "run", wantRun: 1},
			{name: "unbound with missing resources", missingRun: true, missingTask: true},
			{name: "missing run admission", run: "run", missingRun: true},
			{name: "missing task admission", run: "run", phase: "execute", missingTask: true, wantRun: 1},
			{name: "phase accepted", run: "run", phase: "execute", wantRun: 1, wantTask: 1},
			{name: "phase rejected", run: "run", phase: "execute", taskErr: rejected, wantRun: 1, wantTask: 1},
			{name: "run rejected", run: "run", phase: "execute", runErr: rejected, wantRun: 1},
		} {
			t.Run(backend+"/"+tc.name, func(t *testing.T) {
				probe := &workflowAdmissionProbe{runErr: tc.runErr, taskErr: tc.taskErr}
				domains := &WorkflowDomains{Runs: probe, Tasks: probe}
				if tc.missingRun {
					domains.Runs = nil
				}
				if tc.missingTask {
					domains.Tasks = nil
				}
				var prepare func(context.Context, string, *api.WorkerTask) error
				var lookup func(string) (*api.WorkerTask, bool)
				if backend == "sql" {
					q := NewSQLQueue(testdbfixture.Open(t, "store.db"), 1)
					q.SetWorkflowDomains(domains)
					prepare, lookup = q.PrepareEnqueue, q.Get
				} else {
					q := NewInMemoryQueue(1)
					q.SetWorkflowDomains(domains)
					prepare, lookup = q.PrepareEnqueue, q.Get
				}
				task := api.WorkerTask{
					ID: "job", ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID,
					AgentType: "implementer", Prompt: "fixture", Brief: "fixture",
					WorkflowRunID: tc.run, WorkflowPhase: tc.phase,
				}
				err := prepare(t.Context(), testdbseed.DefaultProjectID, &task)
				if tc.run != "" && (tc.missingRun || tc.phase != "" && tc.missingTask) {
					if err == nil {
						t.Fatal("missing workflow authority admitted a task")
					}
					if _, found := lookup(task.ID); found {
						t.Fatal("unvalidated task reached the queue")
					}
				} else if tc.runErr != nil || tc.taskErr != nil {
					if !errors.Is(err, rejected) {
						t.Fatalf("admission error = %v, want rejection", err)
					}
					if _, found := lookup(task.ID); found {
						t.Fatal("rejected task reached the queue")
					}
				} else {
					testutil.FailErr(t, "prepare admitted task", err)
				}
				if probe.runCalls != tc.wantRun || probe.taskCalls != tc.wantTask {
					t.Fatalf("validation calls = %d/%d, want %d/%d", probe.runCalls, probe.taskCalls, tc.wantRun, tc.wantTask)
				}
				if tc.wantTask > 0 && (probe.task.ID != task.ID || probe.task.WorkflowRunID != tc.run || probe.task.WorkflowPhase != tc.phase) {
					t.Fatalf("validator received different task coordinates: %+v", probe.task)
				}
			})
		}
	}
}
