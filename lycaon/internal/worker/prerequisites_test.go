package worker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPrerequisitesWaitForPromotedSuccessfulOutput(t *testing.T) {
	for _, sqlMode := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "sql"}[sqlMode], func(t *testing.T) {
			var q WorkerQueue
			var transition func(string, string, api.WorkerMergeStatus)
			if sqlMode {
				database := testdbfixture.Open(t, "workers.db")
				testdbseed.InsertSession(t, database, "parent", testdbseed.DefaultProjectID)
				queue := NewSQLQueue(database, 2)
				q = queue
				transition = func(id, status string, merge api.WorkerMergeStatus) {
					_, err := database.ExecContext(t.Context(), `UPDATE worker_jobs SET status='complete', result_json=json_object('status',?), merge_status=? WHERE id=?`, status, string(merge), id)
					testutil.FailErr(t, "set producer outcome", err)
				}
			} else {
				queue := NewInMemoryQueue(2)
				q = queue
				transition = func(id, status string, merge api.WorkerMergeStatus) {
					queue.mu.Lock()
					defer queue.mu.Unlock()
					job := queue.jobs[id]
					job.task.Status = api.WorkerStatusComplete
					job.task.Result = &api.WorkerResult{Status: status}
					job.task.MergeStatus = merge
					delete(queue.running, id)
				}
			}
			producer := api.WorkerTask{WorkspacePath: t.TempDir(), ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID, Prompt: "produce module", Brief: "produce module", AgentType: "implementer", Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite}}
			id, err := q.Enqueue(t.Context(), producer)
			testutil.FailErr(t, "enqueue producer", err)
			consumer := producer
			consumer.Brief = "integrate"
			consumer.AfterWorkers = []string{id}
			cid, err := q.Enqueue(t.Context(), consumer)
			testutil.FailErr(t, "enqueue consumer", err)
			claim, err := q.ClaimNext(t.Context(), ClaimRequest{})
			testutil.FailErr(t, "claim producer", err)
			if claim.ID != id {
				t.Fatalf("claimed %s before producer %s", claim.ID, id)
			}
			transition(id, "complete", api.WorkerMergeStatusPending)
			if task, err := q.ClaimNext(t.Context(), ClaimRequest{}); !errors.Is(err, ErrNoPendingJobs) {
				t.Fatalf("premature consumer claim: %+v, %v", task, err)
			}
			transition(id, "complete", api.WorkerMergeStatusMerged)
			claim, err = q.ClaimNext(t.Context(), ClaimRequest{})
			testutil.FailErr(t, "claim ready consumer", err)
			if claim.ID != cid {
				t.Fatalf("claimed %s, want consumer %s", claim.ID, cid)
			}

			for _, outcome := range []struct {
				status string
				merge  api.WorkerMergeStatus
			}{{"partial", api.WorkerMergeStatusMerged}, {"complete", api.WorkerMergeStatusRejected}, {"canceled", api.WorkerMergeStatusAborted}} {
				pid, err := q.Enqueue(t.Context(), producer)
				testutil.FailErr(t, "enqueue unusable producer", err)
				consumer.AfterWorkers = []string{pid}
				first, err := q.Enqueue(t.Context(), consumer)
				testutil.FailErr(t, "enqueue blocked consumer", err)
				consumer.AfterWorkers = []string{first}
				second, err := q.Enqueue(t.Context(), consumer)
				testutil.FailErr(t, "enqueue downstream consumer", err)
				if outcome.status == "canceled" {
					testutil.FailErr(t, "cancel producer", q.Cancel(t.Context(), pid, &api.WorkerResult{Status: "canceled"}))
				} else {
					transition(pid, outcome.status, outcome.merge)
				}
				_, err = q.RecoverExpiredClaims(t.Context())
				testutil.FailErr(t, "settle dependency chain", err)
				for _, blocked := range []string{first, second} {
					task, ok := q.Get(blocked)
					if !ok || task.Status != api.WorkerStatusCanceled || task.Result == nil || task.Result.Status != "canceled" {
						t.Fatalf("unsettled consumer %s: %+v", blocked, task)
					}
				}
			}
		})
	}
}

func TestPrerequisitesRejectCrossSessionAndResumedConsumers(t *testing.T) {
	q := NewInMemoryQueue(2)
	producer := api.WorkerTask{ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID, Prompt: "fixture", Brief: "fixture"}
	id, err := q.Enqueue(t.Context(), producer)
	testutil.FailErr(t, "enqueue producer", err)
	for _, task := range []api.WorkerTask{
		{ParentSessionID: "other", ProjectID: producer.ProjectID, AfterWorkers: []string{id}},
		{ParentSessionID: "parent", ProjectID: producer.ProjectID, AfterWorkers: []string{id}, ChildSessionID: "old-child"},
		{ParentSessionID: "parent", ProjectID: producer.ProjectID, AfterWorkers: []string{id, id}},
	} {
		task.Prompt = "fixture"
		task.Brief = "fixture"
		if _, err := q.Enqueue(t.Context(), task); err == nil {
			t.Fatalf("accepted invalid prerequisites: %+v", task)
		}
	}
}

func TestConsumerCapturesPrimaryAfterProducerPromotion(t *testing.T) {
	canonical := t.TempDir()
	projects := project.NewMemoryRegistry()
	p, err := projects.Create(t.Context(), project.CreateParams{Roots: []project.AttachRootParams{{Path: canonical, Label: "app"}}})
	testutil.FailErr(t, "create project", err)
	q := NewInMemoryQueue(2)
	q.SetProjectStore(projects)
	q.SetWorkerWorkspaceManager(workspace.NewManager(filepath.Join(t.TempDir(), "branches"), filepath.Join(t.TempDir(), "seeds")))
	producer := api.WorkerTask{ProjectID: p.ID, WorkspaceRootID: p.Roots[0].ID, WorkspacePath: canonical, ParentSessionID: "parent", AgentType: "implementer", Prompt: "produce", Brief: "produce", Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite}}
	id, err := q.Enqueue(t.Context(), producer)
	testutil.FailErr(t, "enqueue producer", err)
	consumer := producer
	consumer.AfterWorkers = []string{id}
	cid, err := q.Enqueue(t.Context(), consumer)
	testutil.FailErr(t, "enqueue consumer", err)
	if _, err := q.ClaimWorkerBranch(t.Context(), cid); !errors.Is(err, ErrWorkerPrerequisitesPending) {
		t.Fatalf("premature snapshot: %v", err)
	}
	testutil.FailErr(t, "land producer output", os.WriteFile(filepath.Join(canonical, "producer.go"), []byte("package producer\n"), 0600))
	q.mu.Lock()
	q.jobs[id].task.Status = api.WorkerStatusComplete
	q.jobs[id].task.Result = &api.WorkerResult{Status: "complete"}
	q.jobs[id].task.MergeStatus = api.WorkerMergeStatusMerged
	q.mu.Unlock()
	branch, err := q.ClaimWorkerBranch(t.Context(), cid)
	testutil.FailErr(t, "snapshot ready consumer", err)
	body, err := os.ReadFile(filepath.Join(branch.WorkspaceRoot, "producer.go"))
	testutil.FailErr(t, "read producer module in consumer", err)
	if string(body) != "package producer\n" {
		t.Fatalf("consumer snapshot: %q", body)
	}
	testutil.FailErr(t, "later primary edit", os.WriteFile(filepath.Join(canonical, "producer.go"), []byte("package later\n"), 0600))
	again, err := q.ClaimWorkerBranch(t.Context(), cid)
	testutil.FailErr(t, "reuse consumer snapshot", err)
	body, err = os.ReadFile(filepath.Join(again.WorkspaceRoot, "producer.go"))
	testutil.FailErr(t, "read stable snapshot", err)
	if string(body) != "package producer\n" {
		t.Fatal("consumer snapshot changed after start")
	}
}

func TestProducerPromotionPublishesConsumerReadiness(t *testing.T) {
	database := testdbfixture.Open(t, "events.db")
	testdbseed.InsertSession(t, database, "parent", testdbseed.DefaultProjectID)
	queue := NewSQLQueue(database, 2)
	queue.SetEventOutbox(eventoutbox.New(database, nil))
	producer := api.WorkerTask{WorkspacePath: t.TempDir(), ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID, Prompt: "produce", Brief: "produce", AgentType: "implementer", Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite}}
	id, err := queue.Enqueue(t.Context(), producer)
	testutil.FailErr(t, "enqueue producer", err)
	consumer := producer
	consumer.AfterWorkers = []string{id}
	cid, err := queue.Enqueue(t.Context(), consumer)
	testutil.FailErr(t, "enqueue consumer", err)
	_, err = database.ExecContext(t.Context(), `UPDATE worker_jobs SET status='complete', result_json='{"status":"complete"}' WHERE id=?`, id)
	testutil.FailErr(t, "complete producer", err)
	testutil.FailErr(t, "promote producer", queue.SetMergeStatus(t.Context(), id, api.WorkerMergeStatusMerged))
	var state string
	err = database.QueryRowContext(t.Context(), `SELECT json_extract(data_json,'$.dependencies[0].state') FROM event_outbox WHERE topic='worker' AND facet=? ORDER BY id DESC LIMIT 1`, cid).Scan(&state)
	testutil.FailErr(t, "read consumer event", err)
	if state != "ready" {
		t.Fatalf("consumer readiness = %q", state)
	}
}
