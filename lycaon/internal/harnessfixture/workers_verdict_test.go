package harnessfixture

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestScriptedVerificationFailureIsSettledAndReplayed(t *testing.T) {
	h := newScriptedHarness(t, map[string]string{"app.py": "baseline"})
	controller := h.workers()
	setup := Setup{Dispatches: []DispatchScript{{Label: "App", Mode: "write", Paths: []string{"app.py"}, Stages: []WorkerStage{{Kind: StageComplete, Files: map[string]string{"app.py": "complete"}, Verify: "checks"}}}}}
	testutil.FailErr(t, "install", controller.Install(h.parent.ID, setup, Evidence{}))
	controller.verify = func(context.Context, *api.Session, *api.WorkerTask, string) (*tools.SourceRunCapture, error) {
		return &tools.SourceRunCapture{Verdict: api.SourceVerdictFailed}, nil
	}
	job := h.dispatch("", api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"app.py"}}, nil)
	_, err := controller.Execute(t.Context(), *job, worker.WorkerRunContext{ProjectDir: h.root})
	var failure ScriptedFailure
	if !errors.As(err, &failure) || failure.Code != FailureDeliveryVerification {
		t.Fatalf("negative verification did not settle as worker failure: %v", err)
	}
	if outcome := h.outcome(job.ID); outcome.Kind != string(StageFailed) || outcome.Code != FailureDeliveryVerification {
		t.Fatalf("negative verification was reported as harness error: %+v", outcome)
	}
	restarted := h.workers()
	restarted.verify = func(context.Context, *api.Session, *api.WorkerTask, string) (*tools.SourceRunCapture, error) {
		t.Fatal("settled negative verification was executed again")
		return nil, nil
	}
	bound, _ := h.queue.Get(job.ID)
	_, err = restarted.Execute(t.Context(), *bound, worker.WorkerRunContext{ProjectDir: h.root})
	if !errors.As(err, &failure) || failure.Code != FailureDeliveryVerification {
		t.Fatalf("replay changed the negative verdict: %v", err)
	}
}

func TestScriptedMissingVerificationRemainsAnExecutionError(t *testing.T) {
	for _, verdict := range []string{"", api.SourceVerdictUnverifiable} {
		t.Run(verdict, func(t *testing.T) {
			h := newScriptedHarness(t, map[string]string{"app.py": "baseline"})
			controller := h.workers()
			setup := Setup{Dispatches: []DispatchScript{{Label: "App", Mode: "write", Paths: []string{"app.py"}, Stages: []WorkerStage{{Kind: StageComplete, Files: map[string]string{"app.py": "complete"}, Verify: "checks"}}}}}
			testutil.FailErr(t, "install", controller.Install(h.parent.ID, setup, Evidence{}))
			controller.verify = func(context.Context, *api.Session, *api.WorkerTask, string) (*tools.SourceRunCapture, error) {
				if verdict == "" {
					return nil, nil
				}
				return &tools.SourceRunCapture{Verdict: verdict}, nil
			}
			job := h.dispatch("", api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"app.py"}}, nil)
			_, err := controller.Execute(t.Context(), *job, worker.WorkerRunContext{ProjectDir: h.root})
			if err == nil || h.outcome(job.ID).Kind != "error" {
				t.Fatalf("missing verification was treated as a measured outcome: %v", err)
			}
		})
	}
}
