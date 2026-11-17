package wiring

import (
	"context"
	"maps"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// startableShippedManifests returns every bundled manifest and every bundled
// template expanded and resolved against them, keyed id@version.
func startableShippedManifests(t *testing.T) map[string]workflowdef.Manifest {
	t.Helper()
	bundled, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "workflowdef.RegistryFromDirs", err)
	out := bundled.All()
	templates, err := workflowcomposition.LoadTemplatesFromDir(extpacks.Bundled(config.PlatformFlows.Join("_templates")))
	testutil.FailErr(t, "load workflow templates", err)
	for id, tpl := range templates {
		yaml, err := tpl.Expand(map[string]any{"workflow_id": "wiring-" + id, "question": "Which API surface?"})
		testutil.FailErr(t, "expand template "+id, err)
		raw, err := workflowdef.ParseManifestYAML(yaml)
		testutil.FailErr(t, "parse template "+id, err)
		catalog := maps.Clone(bundled.All())
		catalog[raw.ID+"@"+raw.Version] = raw
		resolved, err := workflowdef.ResolveManifestChain(raw, catalog)
		testutil.FailErr(t, "resolve template "+id, err)
		out[resolved.ID+"@"+resolved.Version] = resolved
	}
	return out
}

// A run parked on a human-input phase refuses every coordinator loop wake, for
// every human-input phase of every shipped workflow and template.
func TestHumanInputScaffoldDeniesCoordinatorLoop(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "loop-wake-human-input.db")
	sessions := store.NewSQL(sqlDB)
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ack"}}}))
	mgr := session.NewHost(sessions, session.Models{Client: rec, Limits: settings.DefaultSessionLimits()}, tools.NewStubRegistry())
	agents := orchestration.NewMemoryAgentRegistry()
	testutil.FailErr(t, "LoadRequiredAgentRegistry", orchestration.LoadRequiredAgentRegistry(ctx, agents))
	mgr.Profiles.SetAgentRegistry(agents)

	all := startableShippedManifests(t)
	wfMgr := workflow.NewManager(workflowpersistence.New(sqlDB), sessions, workflowdef.NewRegistry(all), nil)
	mgr.SetWorkflowDomains(&session.WorkflowDomains{Runs: wfMgr.Store.Runs, Policy: wfMgr.Policy, Ambient: wfMgr.Ambient, Blueprints: wfMgr.Blueprints, Batch: wfMgr.Batch, Slash: wfMgr.Slash, Requests: wfMgr.Requests, Feedback: wfMgr.Feedback, Transcript: wfMgr.Transcript, Asks: wfMgr.Asks, Fanout: wfMgr.Fanout, Phases: wfMgr.Phases, Reports: wfMgr.Reports, Recovery: wfMgr.Recovery, Cleanup: wfMgr})
	mgr.SetLoopWorkflowSource(&loopwake.WorkflowDomains{Runs: wfMgr.Store.Runs, Approvals: wfMgr.Policy, Obligations: wfMgr.Obligations})

	keys := make([]string, 0, len(all))
	for key := range all {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	cells := 0
	for _, key := range keys {
		m := all[key]
		for _, phase := range m.PhaseDefs {
			if phase.OnEnter.RequestUserFeedback == nil {
				continue
			}
			cells++
			t.Run(key+"/"+phase.ID, func(t *testing.T) {
				dir := t.TempDir()
				testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
				sess, err := sessions.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
				testutil.FailErr(t, "create session", err)
				run, err := wfMgr.Starts.StartHuman(ctx, sess.ID, wire.StartWorkflowRunRequest{WorkflowID: m.ID, WorkflowVersion: m.Version})
				testutil.FailErr(t, "start "+key, err)
				run.CurrentPhase = phase.ID
				run.Status = wire.WorkflowRunStatusRunning
				vars, err := workflowphases.ApplyPhaseOnEnter(ctx, workflowphases.PhaseEnterRequest{
					Sessions: sessions, SessionID: sess.ID, Manifest: m, PhaseID: phase.ID,
				})
				testutil.FailErr(t, "ApplyPhaseOnEnter", err)
				testutil.FailErr(t, "CommitState", wfMgr.Store.State.CommitState(ctx, run, dir, vars))
				if !scaffoldvars.HasPendingUserInput(vars) {
					t.Fatal("expected pending user input scaffold")
				}
				allow, reason, err := mgr.Coordinator.Runtime.CoordinatorLoop().Admission.ShouldLoopWake(ctx, sess.ID, anchor.LegFinished)
				testutil.FailErr(t, "ShouldLoopWake", err)
				if allow || reason != "pending_user_input" {
					t.Fatalf("loop wake = %t reason %q during human input phase %q, want refused for pending_user_input", allow, reason, phase.ID)
				}
			})
		}
	}
	if cells == 0 {
		t.Fatal("no shipped workflow declares a human-input phase")
	}
}
