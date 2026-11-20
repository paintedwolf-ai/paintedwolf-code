package wiring

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
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
	h := BuildForTest(t)
	all := startableShippedManifests(t)
	keys := make([]string, 0, len(all))
	for key, m := range all {
		h.RegisterManifest(m)
		keys = append(keys, key)
	}
	sort.Strings(keys)
	ctx := h.OwnerCtx(t, context.Background())
	cells := 0
	for _, key := range keys {
		m := all[key]
		for _, phase := range m.PhaseDefs {
			if phase.OnEnter.RequestUserFeedback == nil {
				continue
			}
			cells++
			t.Run(key+"/"+phase.ID, func(t *testing.T) {
				dir := h.ProjectDir(t, fmt.Sprintf("human-input-%d", cells))
				sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{Posture: wire.SessionPostureSpec}, dir)
				testutil.FailErr(t, "create session", err)
				run, err := h.Workflows.Manager.Starts.StartHuman(ctx, sess.ID, wire.StartWorkflowRunRequest{WorkflowID: m.ID, WorkflowVersion: m.Version, Request: "Plan the change"})
				testutil.FailErr(t, "start "+key, err)
				run.CurrentPhase = phase.ID
				run.Status = wire.WorkflowRunStatusRunning
				vars, err := workflowphases.ApplyPhaseOnEnter(ctx, workflowphases.PhaseEnterRequest{
					Sessions: h.Store, SessionID: sess.ID, Manifest: m, PhaseID: phase.ID,
				})
				testutil.FailErr(t, "ApplyPhaseOnEnter", err)
				testutil.FailErr(t, "CommitState", h.Workflows.Manager.Store.State.CommitState(ctx, run, dir, vars))
				if !scaffoldvars.HasPendingUserInput(vars) {
					t.Fatal("expected pending user input scaffold")
				}
				allow, reason, err := h.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop().Admission.ShouldLoopWake(ctx, sess.ID, anchor.LegFinished)
				testutil.FailErr(t, "ShouldLoopWake", err)
				if allow || reason != "pending_user_input" {
					t.Fatalf("loop wake = %t reason %q during human input phase %q, want refused for pending_user_input", allow, reason, phase.ID)
				}
			})
		}
	}
	if cells == 0 {
		t.Fatal("no shipped workflow or template declares a human-input phase")
	}
}
