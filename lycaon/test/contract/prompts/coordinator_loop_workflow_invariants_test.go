package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	wire "github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
)

func TestHumanInputPhasesLatchPendingOnEnter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for key, m := range workflowfixture.ContractAllResolvedManifests(t) {
		for _, phase := range workflowfixture.HumanInputPhases(m) {
			vars, err := workflowphases.ApplyPhaseOnEnter(ctx, workflowphases.PhaseEnterRequest{Manifest: m, PhaseID: phase.ID})
			if err != nil {
				t.Fatalf("manifest %q phase %q on_enter: %v", key, phase.ID, err)
			}
			if !scaffoldvars.HasPendingUserInput(vars) {
				t.Fatalf("manifest %q phase %q: expected pending user input after on_enter", key, phase.ID)
			}
		}
	}
}

func TestHumanInputScaffoldDeniesCoordinatorLoop(t *testing.T) {
	ctx := context.Background()

	sqlDB := testdbfixture.Open(t, "loop wake-contract.db")

	store := store.NewSQL(sqlDB)
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ack"}}}))
	mgr := session.NewManager(store, rec, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	agents := orchestration.NewMemoryAgentRegistry()
	contractcheck.FailErr(t, "LoadRequiredAgentRegistry", orchestration.LoadRequiredAgentRegistry(ctx, agents))
	mgr.Profiles.SetAgentRegistry(agents)

	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "workflow.RegistryFromDirs failed", err)
	wfStore := workflowpersistence.New(sqlDB)
	wfMgr := workflow.NewManager(wfStore, store, manifestRegistry, nil)
	mgr.SetWorkflowDomains(&session.WorkflowDomains{Runs: wfMgr.Store.Runs, Policy: wfMgr.Policy, Ambient: wfMgr.Ambient, Blueprints: wfMgr.Blueprints, Batch: wfMgr.Batch, Slash: wfMgr.Slash, Requests: wfMgr.Requests, Feedback: wfMgr.Feedback, Transcript: wfMgr.Transcript, Asks: wfMgr.Asks, Fanout: wfMgr.Fanout, Phases: wfMgr.Phases, Reports: wfMgr.Reports, Recovery: wfMgr.Recovery, Cleanup: wfMgr})
	mgr.SetLoopWorkflowSource(&loopwake.WorkflowDomains{Runs: wfMgr.Store.Runs, Approvals: wfMgr.Policy, Obligations: wfMgr.Obligations})

	for key, m := range workflowfixture.ContractAllResolvedManifests(t) {
		phases := workflowfixture.HumanInputPhases(m)
		if len(phases) == 0 {
			continue
		}
		for _, phase := range phases {
			t.Run(key+"/"+phase.ID, func(t *testing.T) {
				dir := t.TempDir()
				testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
				sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
				contractcheck.FailErr(t, "create session in store", err)
				run, err := wfMgr.Starts.Start(ctx, sess.ID, wire.StartWorkflowRunRequest{
					WorkflowID:      m.ID,
					WorkflowVersion: m.Version,
				})
				if err != nil {
					t.Skipf("cannot start run for %s: %v", key, err)
				}
				run.CurrentPhase = phase.ID
				run.Status = wire.WorkflowRunStatusRunning
				vars, err := workflowphases.ApplyPhaseOnEnter(ctx, workflowphases.PhaseEnterRequest{
					Sessions: store, SessionID: sess.ID, Manifest: m, PhaseID: phase.ID,
				})
				contractcheck.FailErr(t, "workflowphases.ApplyPhaseOnEnter failed", err)
				if err := wfMgr.Store.CommitState(ctx, run, dir, vars); err != nil {
					contractcheck.FailErr(t, "wfMgr.Store.CommitState failed", err)
				}
				if !scaffoldvars.HasPendingUserInput(vars) {
					t.Fatal("expected pending user input scaffold")
				}
				allow, reason, err := mgr.Coordinator.Runtime.CoordinatorLoop().ShouldLoopWake(ctx, sess.ID, anchor.LegFinished)
				contractcheck.FailErr(t, "mgr.ShouldLoopWake failed", err)
				if allow {
					t.Fatalf("loop wake allowed during human input phase %q", phase.ID)
				}
				if reason != "pending_user_input" {
					t.Fatalf("reason = %q want pending_user_input", reason)
				}
			})
		}
	}
}
