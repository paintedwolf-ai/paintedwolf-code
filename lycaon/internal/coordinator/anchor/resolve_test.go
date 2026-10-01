package anchor

import "testing"

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
		Selector: Selector{Surface: "phase", Workflow: &wf, Phase: &phase},
	}
	r.add(builtin)
	r.add(workflow)

	got, ok := r.ResolveInform(PhaseEntered, MatchContext{Surface: "phase", Workflow: "recon-pack", Phase: "plan"})
	if !ok || got.Render != "coordinator-fanout-plan" {
		t.Fatalf("want workflow binding, got ok=%v render=%q", ok, bindingRender(got))
	}
	// Different workflow must not leak the recon-pack inject; fall through only if builtin matches.
	got, ok = r.ResolveInform(PhaseEntered, MatchContext{Surface: "phase", Workflow: "options", Phase: "plan"})
	if !ok || got.Render != "coordinator-phase-advanced" {
		t.Fatalf("want builtin fallback for other workflow, got ok=%v render=%q", ok, bindingRender(got))
	}
	// Empty match context: primary/builtin only (no workflow leak).
	got, ok = r.ResolveInform(PhaseEntered, MatchContext{})
	if !ok || got.Tier != "builtin" || got.Render != "coordinator-phase-advanced" {
		t.Fatalf("empty ctx should resolve builtin primary, got ok=%v tier=%q render=%q", ok, bindingTier(got), bindingRender(got))
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
		Selector: Selector{Surface: "phase", Workflow: &wf, Phase: &phase},
	})
	if _, ok := r.ResolveInform(PhaseEntered, MatchContext{}); ok {
		t.Fatal("workflow-only Binding must not resolve under empty MatchContext")
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
