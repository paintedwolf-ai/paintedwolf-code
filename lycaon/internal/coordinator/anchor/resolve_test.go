package anchor

import (
	"context"
	"errors"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSelectorMatches_WorkflowIsolation(t *testing.T) {
	wf := "recon-pack"
	sel := Selector{Workflow: &wf, Surface: "phase"}
	if sel.Matches(MatchContext{}) {
		t.Fatal("workflow-scoped selector must not match empty MatchContext")
	}
	if sel.Matches(MatchContext{Surface: "phase", Workflow: "other"}) {
		t.Fatal("workflow-scoped selector must not match a different workflow")
	}
	if !sel.Matches(MatchContext{Surface: "phase", Workflow: "recon-pack", Phase: "plan"}) {
		t.Fatal("expected match for same workflow id")
	}
}

func TestSelectorMatches_PhaseIsolation(t *testing.T) {
	phase := "execute"
	sel := Selector{Phase: &phase}
	if sel.Matches(MatchContext{Workflow: "recon-pack"}) {
		t.Fatal("phase-scoped selector must not match when ctx.Phase empty")
	}
	if !sel.Matches(MatchContext{Phase: "execute"}) {
		t.Fatal("expected phase match")
	}
}

func TestSelectorMatches_RequiresEveryDeclaredFact(t *testing.T) {
	tool := "read"
	sel := Selector{Surface: "coordinator", Tool: &tool, Profiles: []string{"implement"}, SessionPosture: []string{"build"}}
	if sel.Matches(MatchContext{Surface: "coordinator"}) {
		t.Fatal("selector with declared facts must not match their absence")
	}
	if !sel.Matches(MatchContext{Surface: "coordinator", Tool: "read", Profile: "implement", SessionPosture: "build"}) {
		t.Fatal("selector should match complete matching facts")
	}
}

func TestSelectorMatches_Surfaces(t *testing.T) {
	sel := Selector{Surfaces: []string{"coordinator", "worker"}}
	if sel.Matches(MatchContext{}) {
		t.Fatal("surface selector must not match an absent surface")
	}
	if sel.Matches(MatchContext{Surface: "tool"}) {
		t.Fatal("surface selector must reject an undeclared surface")
	}
	if !sel.Matches(MatchContext{Surface: "worker"}) {
		t.Fatal("surface selector must accept a declared surface")
	}
}

func TestResolveInform_WorkflowTierPreferSpecific(t *testing.T) {
	r := &Registry{
		byAnchor:       make(map[ID][]*Binding),
		informPrimary:  make(map[ID]*Binding),
		renderToAnchor: make(map[string]ID),
	}
	wf := "recon-pack"
	phase := "plan"
	builtin := &Binding{
		On: PhaseEntered, Effect: "inform", Render: "coordinator-phase-advanced", Tier: "builtin",
		Selector: Selector{Surface: "phase"},
	}
	workflow := &Binding{
		On: PhaseEntered, Effect: "inform", Render: "coordinator-fanout-plan", Tier: "workflow",
		Selector:        Selector{Surface: "phase", Workflow: &wf, Phase: &phase},
		workflowVersion: "1.0.0",
	}
	r.add(builtin)
	r.add(workflow)

	got, err := r.ResolveInform(PhaseEntered, MatchContext{Surface: "phase", Workflow: "recon-pack", WorkflowVersion: "1.0.0", Phase: "plan"})
	if err != nil || got == nil || got.Render != "coordinator-fanout-plan" {
		t.Fatalf("want workflow binding, got err=%v render=%q", err, bindingRender(got))
	}
	// Different workflow version must not match workflow binding; falls back to builtin.
	got, err = r.ResolveInform(PhaseEntered, MatchContext{Surface: "phase", Workflow: "recon-pack", WorkflowVersion: "1.0.1", Phase: "plan"})
	if err != nil || got == nil || got.Render != "coordinator-phase-advanced" {
		t.Fatalf("want builtin fallback for version mismatch, got err=%v render=%q", err, bindingRender(got))
	}
	// Different workflow must not leak the recon-pack inject; fall through only if builtin matches.
	got, err = r.ResolveInform(PhaseEntered, MatchContext{Surface: "phase", Workflow: "options", WorkflowVersion: "1.0.0", Phase: "plan"})
	if err != nil || got == nil || got.Render != "coordinator-phase-advanced" {
		t.Fatalf("want builtin fallback for other workflow, got err=%v render=%q", err, bindingRender(got))
	}
	// Empty match context: primary/builtin only (no workflow leak).
	got, err = r.ResolveInform(PhaseEntered, MatchContext{})
	if err != nil || got == nil || got.Tier != "builtin" || got.Render != "coordinator-phase-advanced" {
		t.Fatalf("empty ctx should resolve builtin primary, got err=%v tier=%q render=%q", err, bindingTier(got), bindingRender(got))
	}
}

func TestResolveInform_WorkflowVersionMissingError(t *testing.T) {
	r := &Registry{
		byAnchor:       make(map[ID][]*Binding),
		informPrimary:  make(map[ID]*Binding),
		renderToAnchor: make(map[string]ID),
	}
	wf := "recon-pack"
	phase := "plan"
	r.add(&Binding{
		On: PhaseEntered, Effect: "inform", Render: "coordinator-fanout-plan", Tier: "workflow",
		Selector:        Selector{Surface: "phase", Workflow: &wf, Phase: &phase},
		workflowVersion: "1.0.0",
	})
	// Workflow specified without WorkflowVersion must fail closed with ErrWorkflowVersionMissing.
	got, err := r.ResolveInform(PhaseEntered, MatchContext{Surface: "phase", Workflow: "recon-pack", Phase: "plan"})
	if !errors.Is(err, ErrWorkflowVersionMissing) {
		t.Fatalf("expected ErrWorkflowVersionMissing, got err=%v, binding=%v", err, got)
	}
}

func TestResolveInform_WorkflowOnlyNoEmptyCtxLeak(t *testing.T) {
	r := &Registry{
		byAnchor:       make(map[ID][]*Binding),
		informPrimary:  make(map[ID]*Binding),
		renderToAnchor: make(map[string]ID),
	}
	wf := "recon-pack"
	phase := "plan"
	r.add(&Binding{
		On: PhaseEntered, Effect: "inform", Render: "coordinator-fanout-plan", Tier: "workflow",
		Selector:        Selector{Surface: "phase", Workflow: &wf, Phase: &phase},
		workflowVersion: "1.0.0",
	})
	if got, err := r.ResolveInform(PhaseEntered, MatchContext{}); err != nil || got != nil {
		t.Fatalf("workflow-only Binding must not resolve under empty MatchContext, got binding=%v, err=%v", got, err)
	}
}

func TestRegistry_WorkflowDuplicateRejection(t *testing.T) {
	r := &Registry{
		byAnchor:       make(map[ID][]*Binding),
		informPrimary:  make(map[ID]*Binding),
		renderToAnchor: make(map[string]ID),
	}
	wf := "security-survey"
	phase := "challenge"
	b1 := &Binding{
		On: PhaseEntered, Effect: "inform", Render: "coordinator-security-challenge", Tier: "workflow",
		Selector:        Selector{Surface: "phase", Workflow: &wf, Phase: &phase},
		workflowVersion: "1.0.0",
	}
	b2 := &Binding{
		On: PhaseEntered, Effect: "inform", Render: "coordinator-security-challenge-dup", Tier: "workflow",
		Selector:        Selector{Surface: "phase", Workflow: &wf, Phase: &phase},
		workflowVersion: "1.0.0",
	}
	r.add(b1)
	if err := r.checkWorkflowDuplicate(b2); err == nil {
		t.Fatal("expected duplicate error for same event, surface, phase, workflow, and version")
	}
	// Different version is permitted
	b3 := &Binding{
		On: PhaseEntered, Effect: "inform", Render: "coordinator-security-challenge-v1-0-1", Tier: "workflow",
		Selector:        Selector{Surface: "phase", Workflow: &wf, Phase: &phase},
		workflowVersion: "1.0.1",
	}
	if err := r.checkWorkflowDuplicate(b3); err != nil {
		t.Fatalf("different version should not be duplicate: %v", err)
	}
}

func TestSelector_WorkflowVersionYAMLForbidden(t *testing.T) {
	badYAML := `
surface: phase
workflow: security-survey
workflow_version: 1.0.0
`
	var sel Selector
	if err := yaml.Unmarshal([]byte(badYAML), &sel); err == nil {
		t.Fatal("expected error for YAML containing selector.workflow_version, got nil")
	}
}

func bindingRender(b *Binding) string {
	if b == nil {
		return ""
	}
	return b.Render
}

func bindingTier(b *Binding) string {
	if b == nil {
		return ""
	}
	return b.Tier
}

func TestResolverReleasePreservesReplacement(t *testing.T) {
	oldRegistry, currentRegistry := &Registry{}, &Registry{}
	releaseOld := SetAnchorsFor(func(context.Context, string) *Registry { return oldRegistry })
	releaseCurrent := SetAnchorsFor(func(context.Context, string) *Registry { return currentRegistry })
	t.Cleanup(releaseCurrent)
	releaseOld()
	releaseOld()
	if got := RegistryFor(t.Context(), "session"); got != currentRegistry {
		t.Fatal("old owner removed current registry")
	}
	releaseCurrent()
	if got := RegistryFor(t.Context(), "session"); got == currentRegistry {
		t.Fatal("released registry remains reachable")
	}
}
