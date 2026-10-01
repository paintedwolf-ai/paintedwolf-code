package session

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

type decisionWorkerJobs struct {
	jobLister
	job *api.WorkerTask
}

func (q decisionWorkerJobs) Get(id string) (*api.WorkerTask, bool) {
	return q.job, q.job != nil && q.job.ID == id
}

func TestWorkerDecisionUsesBoundJobInsteadOfRenderedPreamble(t *testing.T) {
	worker := &api.Session{ID: "child", ParentSessionID: "parent", AgentType: "builder"}
	job := &api.WorkerTask{ID: "job", ChildSessionID: worker.ID, ParentSessionID: worker.ParentSessionID, AgentType: worker.AgentType, Brief: "Add Docker support", Prompt: strings.Repeat("host instructions ", 200)}
	mgr := &Manager{workerQueue: decisionWorkerJobs{job: job}}
	history := []api.Message{{ID: "opening", WorkerID: job.ID, Content: strings.Repeat("host instructions ", 200)}}
	for _, jobID := range []string{job.ID, ""} {
		text, ok := mgr.workerDecisionRequest(worker, jobID, "opening", history)
		if !ok || text != job.Brief {
			t.Fatalf("worker request = %q, %v", text, ok)
		}
	}
	job.LegID = "leg"
	job.Prompt = "Add a Dockerfile and run the container"
	if text, ok := mgr.workerDecisionRequest(worker, job.ID, "opening", history); !ok || text != job.Prompt {
		t.Fatalf("delegation request = %q, %v", text, ok)
	}
	job.ChildSessionID = "another child"
	if text, ok := mgr.workerDecisionRequest(worker, job.ID, "opening", history); ok || text != "" {
		t.Fatalf("mismatched job supplied request = %q, %v", text, ok)
	}
	mgr.workerQueue = nil
	if _, ok := mgr.workerDecisionRequest(worker, job.ID, "opening", history); ok {
		t.Fatal("missing job ledger supplied a request")
	}
}
