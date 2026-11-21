package wiring

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAskWorkflowTransitionPreservesReadPhase(t *testing.T) {
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID: "read-then-work", Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID: "read", CompleteWhen: "gates_satisfied", Next: "work",
			Transitions: []workflowdef.PhaseTransitionDef{{
				ID: "implement", To: "work", Actors: []string{workflowdef.TransitionActorHuman},
			}},
		}, {
			ID: "work", CompleteWhen: "gates_satisfied",
			ParallelTask: &workflowdef.ParallelTask{MaxWorkers: 1, MaxWriteWorkers: 1},
		}},
	})
	h := BuildForTest(t, WithManifestRegistry(map[string]workflowdef.Manifest{"read-then-work@1.0.0": manifest}))
	ctx := h.OwnerCtx(t, t.Context())
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Mode: new(api.SessionModeAsk)}, h.ProjectDir(t, "ask-workflow"))
	testutil.FailErr(t, "create Ask session", err)
	manager := h.Workflows.Manager
	run, err := manager.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: manifest.ID, WorkflowVersion: manifest.Version, Request: "Inspect scope.",
	})
	testutil.FailErr(t, "start read phase", err)
	if _, err := manager.Phases.Advance(ctx, run.ID); !errors.Is(err, runstate.ErrAskWorkflowRequiresAgent) {
		t.Fatalf("advance into write phase = %v", err)
	}
	if _, err := manager.Phases.FireTransition(ctx, run.ID, "implement", workflowdef.TransitionActorHuman); !errors.Is(err, runstate.ErrAskWorkflowRequiresAgent) {
		t.Fatalf("choice into write phase = %v", err)
	}
	retained, err := manager.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "load retained workflow", err)
	if retained.CurrentPhase != "read" || retained.Revision != run.Revision {
		t.Fatalf("Ask refusal modified the run: %+v", retained)
	}
	_, err = h.Store.SetMode(ctx, sess.ID, api.SessionModeAgent)
	testutil.FailErr(t, "upgrade session", err)
	started, err := manager.Phases.FireTransition(ctx, run.ID, "implement", workflowdef.TransitionActorHuman)
	testutil.FailErr(t, "enter write phase in Agent", err)
	if started.CurrentPhase != "work" || started.Revision <= retained.Revision {
		t.Fatalf("Agent transition = %+v", started)
	}
}

func TestAskWorkerToolsRefuseMutatingDispatchAndContinuation(t *testing.T) {
	h := BuildForTest(t)
	ctx := h.OwnerCtx(t, t.Context())
	dir := h.ProjectDir(t, "ask-worker")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Mode: new(api.SessionModeAsk)}, dir)
	testutil.FailErr(t, "create Ask session", err)
	tc := coordToolCtx(t, h, ctx, sess, dir)
	tc.Source.AskMode, tc.Source.ReadOnly = true, true
	tc.Identity.ToolCallID = "deny-write"
	_, err = h.ToolRegistry.Run(ctx, "task", TaskToolArgs(orchestration.ProfileImplementer, "Modify file.", "src/main.go"), tc)
	requireAskRejection(t, err)
	assertAskWorkers(t, h, sess, 0)

	tc.Identity.ToolCallID = "start-reader"
	reader := TaskToolArgs(orchestration.ProfileRepoResearcher, "Read the repository.")
	reader["name"] = "reader"
	_, err = h.ToolRegistry.Run(ctx, "task", reader, tc)
	testutil.FailErr(t, "start a read-scoped worker", err)
	tc.Identity.ToolCallID = "deny-reader-to-writer"
	_, err = h.ToolRegistry.Run(ctx, "instruct_worker", map[string]any{
		"worker": "reader", "instructions": "Modify file.", "agent_type": orchestration.ProfileImplementer,
	}, tc)
	requireAskRejection(t, err)
	assertAskWorkers(t, h, sess, 1)
	var profile, scope string
	testutil.FailErr(t, "read worker record", h.DB.QueryRowContext(ctx,
		"SELECT agent_type, scope_mode FROM workers WHERE session_id = ? AND name = 'reader'", sess.ID).Scan(&profile, &scope))
	if profile != orchestration.ProfileRepoResearcher || scope != "read" {
		t.Fatalf("refused instruction changed worker to %s/%s", profile, scope)
	}
}

func requireAskRejection(t *testing.T, err error) {
	t.Helper()
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != tools.AskModeActiveCode {
		t.Fatalf("protected effect refusal = %v", err)
	}
}

func assertAskWorkers(t *testing.T, h *Harness, sess *api.Session, want int) {
	t.Helper()
	for _, relation := range []string{"workers", "worker_jobs"} {
		column := "session_id"
		if relation == "worker_jobs" {
			column = "parent_session_id"
		}
		var count int
		testutil.FailErr(t, "count "+relation, h.DB.QueryRowContext(t.Context(), "SELECT count(*) FROM "+relation+" WHERE "+column+" = ?", sess.ID).Scan(&count))
		if count != want {
			t.Fatalf("%s = %d, want %d", relation, count, want)
		}
	}
}
