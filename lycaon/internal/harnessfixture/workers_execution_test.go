package harnessfixture

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestScriptedParentNeverFallsBackForUnplannedChildren(t *testing.T) {
	h := newScriptedHarness(t, map[string]string{"app.py": "baseline"})
	controller := h.workers()
	setup := Setup{Policy: WorkerPolicyScripted, Overlays: []Overlay{{Label: "Existing", Files: map[string]string{"app.py": "prepared"}}}}
	evidence, err := Prepare(t.Context(), h.queue, h.sessions, h.parent, h.attached.Roots[0], setup, nil)
	testutil.FailErr(t, "prepare", err)
	testutil.FailErr(t, "install", controller.Install(h.parent.ID, setup, evidence))
	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"app.py"}}
	for _, child := range []string{evidence.Overlays[0].ChildSessionID, ""} {
		job := h.dispatch(child, scope, nil)
		_, err := controller.Execute(t.Context(), *job, worker.WorkerRunContext{ProjectDir: h.root})
		if err == nil || h.outcome(job.ID).Code != FailureDispatchUnplanned {
			t.Fatalf("unplanned execution: %v", err)
		}
		bound, _ := h.queue.Get(job.ID)
		_, err = controller.Execute(t.Context(), *bound, worker.WorkerRunContext{ProjectDir: h.root})
		if err == nil || h.fallback.calls != 0 {
			t.Fatalf("resumed unplanned child reached fallback: %v", err)
		}
	}
}

func TestScriptedCancellationDoesNotConsumeDelivery(t *testing.T) {
	h := newScriptedHarness(t, map[string]string{"app.py": "baseline"})
	controller := h.workers()
	setup := Setup{Dispatches: []DispatchScript{{Label: "App", Mode: "write", Paths: []string{"app.py"}, Stages: []WorkerStage{{Kind: StageComplete, Files: map[string]string{"app.py": "complete"}, Verify: "checks"}}}}}
	testutil.FailErr(t, "install", controller.Install(h.parent.ID, setup, Evidence{}))
	job := h.dispatch("", api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"./app.py", "app.py"}}, nil)
	verify := controller.verify
	controller.verify = func(context.Context, *api.Session, *api.WorkerTask, string) (*tools.SourceRunCapture, error) {
		return nil, context.Canceled
	}
	_, err := controller.Execute(t.Context(), *job, worker.WorkerRunContext{ProjectDir: h.root})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled execution: %v", err)
	}
	if _, err := os.Stat(filepath.Join(controller.root, "outcomes", job.ID+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled delivery recorded a successful outcome")
	}
	bound, _ := h.queue.Get(job.ID)
	controller.verify = verify
	_, err = controller.Execute(t.Context(), *bound, worker.WorkerRunContext{ProjectDir: h.root})
	testutil.FailErr(t, "retry canceled delivery", err)
	if h.outcome(job.ID).Kind != string(StageComplete) {
		t.Fatal("retry did not deliver the original stage")
	}
}

func TestScriptedDeliveryErrorIsPrivateAndCannotReportComplete(t *testing.T) {
	h := newScriptedHarness(t, map[string]string{"app.py": "baseline"})
	controller := h.workers()
	setup := Setup{Dispatches: []DispatchScript{{Label: "App", Mode: "write", Paths: []string{"app.py"}, Stages: []WorkerStage{{Kind: StageComplete, Files: map[string]string{"app.py": "complete"}, Verify: "checks"}}}}}
	testutil.FailErr(t, "install", controller.Install(h.parent.ID, setup, Evidence{}))
	controller.verify = func(context.Context, *api.Session, *api.WorkerTask, string) (*tools.SourceRunCapture, error) {
		return nil, errors.New("private fixture verification failed")
	}
	job := h.dispatch("", api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"app.py"}}, nil)
	_, err := controller.Execute(t.Context(), *job, worker.WorkerRunContext{ProjectDir: h.root})
	if err == nil || err.Error() != ScriptedFailureMessage {
		t.Fatalf("leaked delivery error: %v", err)
	}
	outcome := h.outcome(job.ID)
	if outcome.Kind != "error" || outcome.Code != FailureExecution || outcome.Detail != "private fixture verification failed" {
		t.Fatalf("dishonest receipt: %+v", outcome)
	}
}

func TestAnsweredDecisionDeliveryErrorDoesNotBecomeComplete(t *testing.T) {
	h := newScriptedHarness(t, map[string]string{"app.py": "baseline"})
	controller := h.workers()
	delivery := Delivery{Files: map[string]string{"app.py": "complete"}, Verify: "checks"}
	setup := Setup{Dispatches: []DispatchScript{{Label: "App", Mode: "write", Paths: []string{"app.py"}, Stages: []WorkerStage{{
		Kind: StageNeedsDecision, Question: "Which option?", Options: []string{"first", "second"},
		Outcomes: map[string]Delivery{"first": delivery, "second": delivery},
	}}}}}
	testutil.FailErr(t, "install decision fixture", controller.Install(h.parent.ID, setup, Evidence{}))
	job := h.dispatch("", api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"app.py"}}, nil)
	result, err := controller.Execute(t.Context(), *job, worker.WorkerRunContext{ProjectDir: h.root})
	testutil.FailErr(t, "reach decision", err)
	bound, _ := h.queue.Get(job.ID)
	job.ChildSessionID = bound.ChildSessionID
	_, err = h.queue.Complete(t.Context(), job, result)
	testutil.FailErr(t, "hold decision job", err)
	testutil.FailErr(t, "answer decision", h.sessions.AppendMessages(t.Context(), bound.ChildSessionID, worker.DecisionAnswerMessage("first", "coordinator")))
	controller.verify = func(context.Context, *api.Session, *api.WorkerTask, string) (*tools.SourceRunCapture, error) {
		return nil, errors.New("decision delivery verification failed")
	}
	_, err = controller.Execute(t.Context(), *job, worker.WorkerRunContext{ProjectDir: h.root})
	if err == nil || err.Error() != ScriptedFailureMessage {
		t.Fatalf("decision delivery error = %v", err)
	}
	outcome := h.outcome(job.ID)
	if outcome.Kind != "error" || outcome.Code != FailureExecution || outcome.Detail != "decision delivery verification failed" {
		t.Fatalf("failed delivery receipt = %+v", outcome)
	}
}

func TestIndependentScriptedLegsVerifyConcurrently(t *testing.T) {
	h := newScriptedHarness(t, map[string]string{"app.py": "baseline", "lib.py": "baseline"})
	controller := h.workers()
	setup := Setup{}
	for _, path := range []string{"app.py", "lib.py"} {
		setup.Dispatches = append(setup.Dispatches, DispatchScript{Label: path, Mode: "write", Paths: []string{path}, Stages: []WorkerStage{{Kind: StageComplete, Files: map[string]string{path: "complete"}, Verify: "checks"}}})
	}
	testutil.FailErr(t, "install", controller.Install(h.parent.ID, setup, Evidence{}))
	var entered atomic.Int32
	release := make(chan struct{})
	controller.verify = func(ctx context.Context, _ *api.Session, _ *api.WorkerTask, _ string) (*tools.SourceRunCapture, error) {
		if entered.Add(1) == 2 {
			close(release)
		}
		select {
		case <-release:
			return &tools.SourceRunCapture{Verdict: api.SourceVerdictPassed}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	results := make(chan error, 2)
	for _, path := range []string{"app.py", "lib.py"} {
		job := h.dispatch("", api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{path}}, nil)
		go func() {
			_, err := controller.Execute(ctx, *job, worker.WorkerRunContext{ProjectDir: h.root})
			results <- err
		}()
	}
	for range 2 {
		testutil.FailErr(t, "independent leg delivery", <-results)
	}
}

func TestOutcomeHistoryRetainsTheDecisionTransition(t *testing.T) {
	h := newScriptedHarness(t, map[string]string{"app.py": "baseline"})
	w := h.workers()
	for _, kind := range []string{"needs_decision", "delivered"} {
		testutil.FailErr(t, "record transition", w.record(Outcome{JobID: "job", ChildSessionID: "child", Kind: kind}))
	}
	body, err := os.ReadFile(filepath.Join(w.root, "outcomes", "job.json"))
	testutil.FailErr(t, "read history", err)
	var history []Outcome
	testutil.FailErr(t, "decode history", json.Unmarshal(body, &history))
	if len(history) != 2 || history[0].Kind != "needs_decision" || history[1].Kind != "delivered" {
		t.Fatalf("lost decision history: %+v", history)
	}
}
