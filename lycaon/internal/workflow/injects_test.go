package workflow_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestEffectiveAnchorRegistryIncludesWorkflowInjects(t *testing.T) {
	reg, err := anchor.LoadRegistryFromConfigRoot()
	testutil.FailErr(t, "LoadRegistryFromConfigRoot", err)

	b, err := reg.ResolveInform(anchor.PhaseEntered, anchor.MatchContext{
		Surface:         "phase",
		Phase:           "plan",
		Workflow:        "recon-pack",
		WorkflowVersion: "1.0.0",
	})
	if err != nil || b == nil || b.Render != "coordinator-recon-pack-plan" {
		t.Fatalf("recon-pack plan inject: err=%v render=%q tier=%q", err, bindingRender(b), bindingTier(b))
	}
	if b.Tier != "workflow" {
		t.Fatalf("tier = %q want workflow", b.Tier)
	}

	// 1.0.0 run gets 1.0.0 prompt
	challenge100, err := reg.ResolveInform(anchor.PhaseEntered, anchor.MatchContext{
		Surface: "phase", Phase: "challenge", Workflow: "security-survey", WorkflowVersion: "1.0.0",
	})
	if err != nil || challenge100 == nil || challenge100.Render != "coordinator-security-challenge" {
		t.Fatalf("security challenge 1.0.0 inject: err=%v render=%q", err, bindingRender(challenge100))
	}

	// 2.0.0 run gets 2.0.0 prompt
	challenge200, err := reg.ResolveInform(anchor.PhaseEntered, anchor.MatchContext{
		Surface: "phase", Phase: "challenge", Workflow: "security-survey", WorkflowVersion: "2.0.0",
	})
	if err != nil || challenge200 == nil || challenge200.Render != "coordinator-security-challenge" {
		t.Fatalf("security challenge 2.0.0 inject: err=%v render=%q", err, bindingRender(challenge200))
	}

	for phase, render := range map[string]string{
		"execute":    "coordinator-fanout-execute",
		"reconcile":  "coordinator-recon-reconcile",
		"drill_plan": "coordinator-recon-drill-plan",
		"drill":      "coordinator-fanout-execute",
		"report":     "coordinator-topology-synthesis",
	} {
		binding, err := reg.ResolveInform(anchor.PhaseEntered, anchor.MatchContext{
			Surface:         "phase",
			Phase:           phase,
			Workflow:        "recon-pack",
			WorkflowVersion: "1.0.0",
		})
		if err != nil || binding == nil || binding.Render != render {
			t.Fatalf("recon-pack %s inject: err=%v render=%q want %q", phase, err, bindingRender(binding), render)
		}
	}

	// Isolation: options does not see recon-pack's plan inject.
	b, err = reg.ResolveInform(anchor.PhaseEntered, anchor.MatchContext{
		Surface:         "phase",
		Phase:           "plan",
		Workflow:        "options",
		WorkflowVersion: "1.0.0",
	})
	if err != nil {
		t.Fatalf("resolve options inject: %v", err)
	}
	if b != nil && b.Render == "coordinator-fanout-plan" {
		t.Fatal("recon-pack inject leaked into options session")
	}
}

func TestReconPackProgressivePhaseShape(t *testing.T) {
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	manifest, err := reg.Get("recon-pack", "1.0.0")
	testutil.FailErr(t, "Get recon-pack", err)

	wantNext := map[string]string{
		"plan":       "execute",
		"execute":    "reconcile",
		"reconcile":  "report",
		"drill_plan": "drill",
		"drill":      "report",
		"report":     "done",
	}
	for phaseID, next := range wantNext {
		phase, ok := manifest.PhaseByID(phaseID)
		if !ok {
			t.Fatalf("recon-pack missing phase %q", phaseID)
		}
		if phase.Next != next {
			t.Fatalf("recon-pack phase %q next = %q want %q", phaseID, phase.Next, next)
		}
	}

	reconcile, _ := manifest.PhaseByID("reconcile")
	if reconcile.CoordinatorSurface != "recon_reconcile" {
		t.Fatalf("reconcile surface = %q", reconcile.CoordinatorSurface)
	}
	for _, transitionID := range []string{"deepen", "report"} {
		edge, ok := reconcile.TransitionByID(transitionID)
		if !ok {
			t.Fatalf("reconcile missing transition %q", transitionID)
		}
		if len(edge.Actors) != 1 || edge.Actors[0] != workflowdef.TransitionActorCoordinator {
			t.Fatalf("reconcile transition %q actors = %v", transitionID, edge.Actors)
		}
	}
}

func bindingRender(b *anchor.Binding) string {
	if b == nil {
		return ""
	}
	return b.Render
}

func bindingTier(b *anchor.Binding) string {
	if b == nil {
		return ""
	}
	return b.Tier
}
