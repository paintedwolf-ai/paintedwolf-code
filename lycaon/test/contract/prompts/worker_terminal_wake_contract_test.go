package contract

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

// wakeCounter stands in for the parent session behind SessionOutcomeBridge and
// counts each way the bridge can wake the coordinator.
type wakeCounter struct {
	legID      string
	afterTerm  atomic.Int32
	legWakes   atomic.Int32
	terminals  atomic.Int32
	closureArm atomic.Int32
}

func (w *wakeCounter) TaskByID(string) (workeroutcomes.SummaryInput, bool) {
	return workeroutcomes.SummaryInput{ParentSessionID: "parent-1", LegID: w.legID}, true
}
func (*wakeCounter) ProjectResult(_ context.Context, _ workeroutcomes.SummaryInput, r api.WorkerResult) (string, error) {
	return r.Status, nil
}
func (*wakeCounter) ProjectFailure(context.Context, workeroutcomes.SummaryInput, error) error {
	return nil
}
func (*wakeCounter) RecordTerminalProof(context.Context, string, string, string) error { return nil }
func (*wakeCounter) EnvelopeForTerminal(context.Context, string, string) anchor.Envelope {
	return anchor.Envelope{}
}
func (w *wakeCounter) Terminal(context.Context, string, string) { w.terminals.Add(1) }
func (w *wakeCounter) AfterTerminal(context.Context, string, string, anchor.Envelope) {
	w.afterTerm.Add(1)
}
func (w *wakeCounter) NudgeLegFinished(context.Context, string, time.Time, string) { w.legWakes.Add(1) }
func (*wakeCounter) ShouldNudge(context.Context, string, string, string) bool      { return true }
func (w *wakeCounter) Arm(context.Context, string, string)                         { w.closureArm.Add(1) }

func (w *wakeCounter) bridge() *worker.SessionOutcomeBridge {
	return &worker.SessionOutcomeBridge{Workers: w, Loop: w, Results: w, State: w, Closure: w}
}

// Every outcome-projection method wakes the parent coordinator once for an
// outcome the parent must react to, and never for one it initiated or that is
// still in flight. A delegated leg wakes through the leg channel.
func TestOutcomeProjectionNudgesOnEveryMethod(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	statuses := api.AllWorkerSummaryStatuses()
	if len(statuses) == 0 {
		t.Fatal("no worker summary statuses")
	}
	for _, status := range statuses {
		want := int32(1)
		if status == api.WorkerSummaryStatusCanceled || status == api.WorkerSummaryStatusHeld {
			want = 0
		}
		w := &wakeCounter{}
		testutil.FailErr(t, "OnWorkerComplete "+string(status), w.bridge().OnWorkerComplete(ctx, "job-1", api.WorkerResult{Status: string(status)}))
		if got := w.afterTerm.Load(); got != want {
			t.Errorf("OnWorkerComplete(%s) woke the coordinator %d times, want %d", status, got, want)
		}
		leg := &wakeCounter{legID: "leg-1"}
		testutil.FailErr(t, "leg OnWorkerComplete "+string(status), leg.bridge().OnWorkerComplete(ctx, "job-1", api.WorkerResult{Status: string(status)}))
		if leg.legWakes.Load() != 1 || leg.afterTerm.Load() != 0 {
			t.Errorf("leg OnWorkerComplete(%s) leg wakes %d, coordinator wakes %d; want 1 and 0", status, leg.legWakes.Load(), leg.afterTerm.Load())
		}
	}
	failed := &wakeCounter{}
	testutil.FailErr(t, "OnWorkerFailed", failed.bridge().OnWorkerFailed(ctx, "job-1", errors.New("worker crashed")))
	if got := failed.afterTerm.Load(); got != 1 {
		t.Errorf("OnWorkerFailed woke the coordinator %d times, want 1", got)
	}
	canceled := &wakeCounter{}
	testutil.FailErr(t, "OnWorkerFailed canceled", canceled.bridge().OnWorkerFailed(ctx, "job-1", context.Canceled))
	if got := canceled.afterTerm.Load(); got != 0 {
		t.Errorf("OnWorkerFailed(canceled) woke the coordinator %d times, want 0", got)
	}
}

// terminalExecutor ends each job the way its prompt names.
type terminalExecutor struct{}

func (terminalExecutor) Execute(_ context.Context, task api.WorkerTask, _ worker.WorkerRunContext) (api.WorkerResult, error) {
	switch task.Prompt {
	case "fail":
		return api.WorkerResult{}, &worker.PermanentExecutionError{Err: errors.New("fixture failure")}
	case "cancel":
		return api.WorkerResult{Status: "canceled"}, nil
	default:
		return api.WorkerResult{Status: "complete"}, nil
	}
}

func (terminalExecutor) AbortWorkerRuntime(context.Context, api.WorkerTask) error { return nil }

type outcomeEvent struct{ jobID, kind, status string }

// outcomeLog reports each delivered outcome on a channel.
type outcomeLog struct{ events chan outcomeEvent }

func (l outcomeLog) OnWorkerComplete(_ context.Context, jobID string, r api.WorkerResult) error {
	l.events <- outcomeEvent{jobID, "complete", r.Status}
	return nil
}
func (l outcomeLog) OnWorkerFailed(_ context.Context, jobID string, _ error) error {
	l.events <- outcomeEvent{jobID, "failed", ""}
	return nil
}
func (outcomeLog) OnOutcomeDelivered(context.Context, api.WorkerTask) {}

// Every terminal queue transition the poller commits publishes its outcome:
// completion, permanent failure and cancellation each reach the recorder once.
func TestWorkerPollerTerminalQueueOpsNotifyOutcomes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "poller-outcomes.db")
	parent, err := store.NewSQL(sqlDB).Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	queue := worker.NewSQLQueue(sqlDB, 3)
	log := outcomeLog{events: make(chan outcomeEvent, 3)}
	cfg := worker.DefaultWorkersConfig()
	poller := worker.NewLocalWorkerPoller(queue, terminalExecutor{}, cfg, log)

	want := map[string]outcomeEvent{}
	for _, tc := range []outcomeEvent{{kind: "complete", status: "complete"}, {kind: "failed"}, {kind: "complete", status: "canceled"}} {
		prompt := map[string]string{"": "fail", "complete": "complete", "canceled": "cancel"}[tc.status]
		id, err := queue.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
			Prompt: prompt, Brief: prompt, ParentSessionID: parent.ID, ProjectID: testdbseed.DefaultProjectID,
			ExecutionTarget: api.ExecutionTargetLocal,
		})
		testutil.FailErr(t, "enqueue "+prompt, err)
		tc.jobID = id
		want[id] = tc
	}

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- poller.Run(runCtx) }()
	got := map[string]outcomeEvent{}
	for len(got) < len(want) {
		ev := <-log.events
		if _, dup := got[ev.jobID]; dup {
			t.Fatalf("outcome for %s published twice", ev.jobID)
		}
		got[ev.jobID] = ev
	}
	stop()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("poller stopped with %v", err)
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("job %s outcome = %+v, want %+v", id, got[id], w)
		}
	}
}
