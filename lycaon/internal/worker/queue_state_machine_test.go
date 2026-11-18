package worker_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
	"pgregory.net/rapid"
)

// The model mirrors only durable facts; tokens are compared to what the queue handed out.
type modelJob struct {
	id              string
	status          api.WorkerStatus
	token           string
	attempt         int
	cancelRequested bool
}

const (
	queueMachineMaxRunning = 2
	queueMachineAttempts   = 3
)

var queueMachineClaimants = []string{"claimant-a", "claimant-b"}

type queueMachine struct {
	t      *testing.T
	path   string
	store  *db.Store
	queue  *worker.SQLQueue
	jobs   []*modelJob
	held   map[string][]api.WorkerTask
	nextAt time.Time
}

func newQueueMachine(t *testing.T, path string) *queueMachine {
	m := &queueMachine{
		t:      t,
		path:   path,
		held:   map[string][]api.WorkerTask{},
		nextAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	m.store = testdbfixture.OpenPath(t, path)
	testdbseed.InsertSession(t, m.store, "parent", testdbseed.DefaultProjectID)
	m.queue = worker.NewSQLQueue(m.store, queueMachineMaxRunning)
	return m
}

func (m *queueMachine) job(id string) *modelJob {
	for _, job := range m.jobs {
		if job.id == id {
			return job
		}
	}
	return nil
}

func (m *queueMachine) running() int {
	n := 0
	for _, job := range m.jobs {
		if job.status == api.WorkerStatusRunning {
			n++
		}
	}
	return n
}

func (m *queueMachine) enqueue(rt *rapid.T) {
	id := fmt.Sprintf("job-%03d", len(m.jobs))
	m.nextAt = m.nextAt.Add(time.Second)
	got, err := m.queue.EnqueueWithProjectID(context.Background(), testdbseed.DefaultProjectID, api.WorkerTask{
		ID: id, ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID,
		Prompt: "fixture", Brief: "fixture", ExecutionTarget: api.ExecutionTargetLocal, CreatedAt: m.nextAt,
	})
	if err != nil || got != id {
		rt.Fatalf("enqueue %s = %q, %v", id, got, err)
	}
	m.jobs = append(m.jobs, &modelJob{id: id, status: api.WorkerStatusPending})
}

func (m *queueMachine) claim(rt *rapid.T) {
	claimant := rapid.SampledFrom(queueMachineClaimants).Draw(rt, "claimant")
	var want *modelJob
	for _, job := range m.jobs {
		if job.status == api.WorkerStatusPending && !job.cancelRequested {
			want = job
			break
		}
	}
	claimed, err := m.queue.ClaimNext(context.Background(), worker.ClaimRequest{
		ProjectID: testdbseed.DefaultProjectID, ClaimedBy: claimant, ExecutionTarget: api.ExecutionTargetLocal,
	})
	switch {
	case m.running() >= queueMachineMaxRunning:
		if !errors.Is(err, worker.ErrMaxWorkers) {
			rt.Fatalf("claim at capacity = %+v, %v; want ErrMaxWorkers", claimed, err)
		}
		return
	case want == nil:
		if !errors.Is(err, worker.ErrNoPendingJobs) {
			rt.Fatalf("claim with nothing claimable = %+v, %v; want ErrNoPendingJobs", claimed, err)
		}
		return
	}
	if err != nil {
		rt.Fatalf("claim: %v", err)
	}
	if claimed.ID != want.id || claimed.ClaimedBy != claimant || claimed.ClaimToken == "" || claimed.ClaimToken == want.token {
		rt.Fatalf("claimed %s by %q token %q; want %s by %s with a fresh token (previous %q)",
			claimed.ID, claimed.ClaimedBy, claimed.ClaimToken, want.id, claimant, want.token)
	}
	want.status, want.token = api.WorkerStatusRunning, claimed.ClaimToken
	want.attempt++
	m.held[claimant] = append(m.held[claimant], *claimed)
}

// pickHeld draws one claim a claimant still holds, current or stale.
func (m *queueMachine) pickHeld(rt *rapid.T) (*api.WorkerTask, *modelJob) {
	claimant := rapid.SampledFrom(queueMachineClaimants).Draw(rt, "claimant")
	held := m.held[claimant]
	if len(held) == 0 {
		rt.Skip("claimant holds no claim")
	}
	task := held[rapid.IntRange(0, len(held)-1).Draw(rt, "claim")]
	return &task, m.job(task.ID)
}

func (job *modelJob) claimCurrent(token string) bool {
	return job.status == api.WorkerStatusRunning && job.token == token && !job.cancelRequested
}

func (m *queueMachine) settle(rt *rapid.T, name string, to api.WorkerStatus, call func(*api.WorkerTask) (bool, error)) {
	task, job := m.pickHeld(rt)
	want := job.claimCurrent(task.ClaimToken)
	won, err := call(task)
	if err != nil {
		rt.Fatalf("%s %s: %v", name, task.ID, err)
	}
	if won != want {
		rt.Fatalf("%s %s with token %q won=%v; want %v (model %+v)", name, task.ID, task.ClaimToken, won, want, *job)
	}
	if won {
		job.status, job.token = to, ""
	}
}

func (m *queueMachine) complete(rt *rapid.T) {
	m.settle(rt, "complete", api.WorkerStatusComplete, func(task *api.WorkerTask) (bool, error) {
		return m.queue.Complete(context.Background(), task, api.WorkerResult{Status: "complete", Summary: "done"})
	})
}

func (m *queueMachine) fail(rt *rapid.T) {
	m.settle(rt, "fail", api.WorkerStatusFailed, func(task *api.WorkerTask) (bool, error) {
		return m.queue.Fail(context.Background(), task, errors.New("fixture failure"))
	})
}

func (m *queueMachine) retry(rt *rapid.T) {
	m.settle(rt, "retry", api.WorkerStatusPending, func(task *api.WorkerTask) (bool, error) {
		return m.queue.Retry(context.Background(), task, errors.New("fixture retry"))
	})
}

func (m *queueMachine) renew(rt *rapid.T) {
	task, job := m.pickHeld(rt)
	won, err := m.queue.RenewClaim(context.Background(), task.ID, task.ClaimToken)
	if err != nil || won != job.claimCurrent(task.ClaimToken) {
		rt.Fatalf("renew %s won=%v err=%v; model %+v", task.ID, won, err, *job)
	}
}

func (m *queueMachine) fenceClaim(rt *rapid.T) {
	task, job := m.pickHeld(rt)
	want := job.claimCurrent(task.ClaimToken)
	won, err := m.queue.RequestClaimCancellation(context.Background(), task)
	if err != nil || won != want {
		rt.Fatalf("claim fence %s won=%v err=%v; want %v (model %+v)", task.ID, won, err, want, *job)
	}
	if won {
		job.cancelRequested = true
	}
}

func (m *queueMachine) drawJob(rt *rapid.T) *modelJob {
	if len(m.jobs) == 0 {
		rt.Skip("no job")
	}
	return m.jobs[rapid.IntRange(0, len(m.jobs)-1).Draw(rt, "job")]
}

func (m *queueMachine) fence(rt *rapid.T) {
	job := m.drawJob(rt)
	want := !job.status.IsTerminal()
	won, err := m.queue.RequestCancellation(context.Background(), job.id)
	if err != nil || won != want {
		rt.Fatalf("fence %s won=%v err=%v; want %v (model %+v)", job.id, won, err, want, *job)
	}
	if won {
		job.cancelRequested = true
	}
}

func (m *queueMachine) cancel(rt *rapid.T) {
	job := m.drawJob(rt)
	if err := m.queue.Cancel(context.Background(), job.id, &api.WorkerResult{Status: "canceled"}); err != nil {
		rt.Fatalf("cancel %s: %v", job.id, err)
	}
	if !job.status.IsTerminal() {
		job.status, job.token, job.cancelRequested = api.WorkerStatusCanceled, "", true
	}
}

// expireAndRecover stands in for every claimant losing its host: all leases lapse.
func (m *queueMachine) expireAndRecover(rt *rapid.T) {
	ctx := context.Background()
	past := timelayout.Format(time.Now().UTC().Add(-time.Minute))
	if _, err := m.store.ExecContext(ctx, `UPDATE worker_jobs SET lease_expires_at = ? WHERE status = 'running'`, past); err != nil {
		rt.Fatalf("expire leases: %v", err)
	}
	if _, err := m.queue.RecoverExpiredClaims(ctx); err != nil {
		rt.Fatalf("recover expired claims: %v", err)
	}
	for _, job := range m.jobs {
		switch {
		case job.status.IsTerminal():
		case job.cancelRequested:
			job.status, job.token = api.WorkerStatusCanceled, ""
		case job.status != api.WorkerStatusRunning:
		case job.attempt < queueMachineAttempts:
			job.status, job.token = api.WorkerStatusPending, ""
		default:
			job.status, job.token = api.WorkerStatusFailed, ""
		}
	}
}

// restart closes the database and builds a new queue over the same file.
// Claimants keep the claims they held, so stale settlements are exercised.
func (m *queueMachine) restart(rt *rapid.T) {
	if err := m.store.Close(); err != nil {
		rt.Fatalf("close database: %v", err)
	}
	m.store = testdbfixture.OpenPath(m.t, m.path)
	m.queue = worker.NewSQLQueue(m.store, queueMachineMaxRunning)
}

func (m *queueMachine) check(rt *rapid.T) {
	running := 0
	for _, job := range m.jobs {
		got, ok := m.queue.Get(job.id)
		if !ok {
			rt.Fatalf("job %s missing", job.id)
		}
		if got.Status != job.status || got.Attempt != job.attempt || got.ClaimToken != job.token {
			rt.Fatalf("job %s = status %s attempt %d token %q; model %+v", job.id, got.Status, got.Attempt, got.ClaimToken, *job)
		}
		if got.Status == api.WorkerStatusRunning {
			running++
		}
	}
	if running > queueMachineMaxRunning {
		rt.Fatalf("%d running jobs exceed cap %d", running, queueMachineMaxRunning)
	}
}

// Two claimants drive the SQL queue through claim, settle, fence, cancel,
// lease recovery and restart; every durable job row must match the model.
func TestSQLWorkerQueueStateMachine(t *testing.T) {
	dir := t.TempDir()
	run := 0
	rapid.Check(t, func(rt *rapid.T) {
		run++
		m := newQueueMachine(t, filepath.Join(dir, fmt.Sprintf("queue-%d.db", run)))
		rt.Repeat(map[string]func(*rapid.T){
			"enqueue":          m.enqueue,
			"claim":            m.claim,
			"complete":         m.complete,
			"fail":             m.fail,
			"retry":            m.retry,
			"renew":            m.renew,
			"fenceClaim":       m.fenceClaim,
			"fence":            m.fence,
			"cancel":           m.cancel,
			"expireAndRecover": m.expireAndRecover,
			"restart":          m.restart,
			"":                 m.check,
		})
	})
}
