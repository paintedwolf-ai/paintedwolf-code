package workflow

import (
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowinputs "github.com/lycaon/lycaon/internal/workflow/inputs"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
)

// An ask injected while turn-boundary batch events write the same scaffold blob
// keeps its pending entry, so the later resolve does not fail feedback_not_pending.
func TestAskInjectSurvivesConcurrentBatchWrites(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		"ask-user-host@1.0.0": askUserTestManifest(),
	})
	ctx := workflowCaller(t, mgr)
	run, err := startRun(ctx, mgr, "sess-1", "ask-user-host", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	// Turn-boundary writers hammering the same run vars.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		seq := 1
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = mgr.Batch.ApplyCoordinatorBatchEvent(ctx, "sess-1", batch.EventVisibleUserMessage, seq)
			seq++
		}
	}()

	handle, err := mgr.Asks.RequestUserInput(ctx, "sess-1", workflowinputs.UserInputRequest{
		Prompt:       "Which layout should the CLI use?",
		ResponseType: workflowdef.FeedbackResponseText,
	})
	testutil.FailErr(t, "RequestUserInput", err)
	close(stop)
	wg.Wait()

	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	pf, ok := runstate.PendingFeedbackFromVars(vars)
	if !ok || pf.PhaseID != handle.PhaseID {
		t.Fatalf("pending ask lost under concurrent batch writes: pf=%+v ok=%v", pf, ok)
	}

	// The resolve lands instead of a 409.
	if _, err := mgr.Feedback.ResolveUserFeedback(ctx, "sess-1", run.ID, handle.PhaseID, "single main.go"); err != nil {
		t.Fatalf("resolve after concurrent writes: %v", err)
	}
}

func askUserTestManifest() workflowdef.Manifest {
	return workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "ask-user-host",
		Version: "1.0.0",
		Controls: workflowdef.ManifestControls{
			PhaseAdvance: workflowdef.PhaseAdvanceHost,
		},
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:            "work",
			ActivityLabel: "Waiting for input",
			CompleteWhen:  "user_feedback_received:work",
			Next:          "done",
		}, {ID: "done", ActivityLabel: "Done"}},
	})
}
