package phases_test

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAskWorkflowParksBeforeAgentPhase(t *testing.T) {
	mgr, sessions, _, _ := testManagerWithRegistry(t)
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID: "read-then-work", Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "read", CompleteWhen: "gates_satisfied", Next: "work"},
			{
				ID: "work", CompleteWhen: "gates_satisfied",
				ParallelTask: &workflowdef.ParallelTask{MaxWorkers: 1, MaxWriteWorkers: 1},
			},
		},
	})
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		workflowdef.ManifestKey(manifest.ID, manifest.Version): manifest,
	})
	ctx := workflowCaller(t, mgr)
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{Mode: new(api.SessionModeAsk)}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create Ask session", err)
	run, err := startRun(ctx, mgr, sess.ID, manifest.ID, manifest.Version)
	testutil.FailErr(t, "start read phase", err)
	if run.CurrentPhase != "read" {
		t.Fatalf("starting phase = %q, want read", run.CurrentPhase)
	}

	if _, err := mgr.Phases.Advance(ctx, run.ID); !errors.Is(err, runstate.ErrAskWorkflowRequiresAgent) {
		t.Fatalf("Advance error = %v, want Ask phase refusal", err)
	}
	held, err := mgr.Phases.TryAutoAdvance(ctx, run.ID)
	testutil.FailErr(t, "try automatic advance in Ask", err)
	if held.CurrentPhase != "read" || held.Revision != run.Revision {
		t.Fatalf("automatic advance changed the Ask run: phase=%q revision=%d", held.CurrentPhase, held.Revision)
	}
	retained, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "reload retained run", err)
	if retained.CurrentPhase != "read" || retained.Revision != run.Revision {
		t.Fatalf("stored Ask run changed: phase=%q revision=%d", retained.CurrentPhase, retained.Revision)
	}

	_, err = sessions.SetMode(ctx, sess.ID, api.SessionModeAgent)
	testutil.FailErr(t, "switch session to Agent", err)
	advanced, err := mgr.Phases.TryAutoAdvance(ctx, run.ID)
	testutil.FailErr(t, "advance in Agent", err)
	if advanced.CurrentPhase != "work" || advanced.Revision <= retained.Revision {
		t.Fatalf("automatic advance did not enter the write phase in Agent: phase=%q revision=%d", advanced.CurrentPhase, advanced.Revision)
	}
}
