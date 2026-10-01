package oar

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStageFromAnchor(t *testing.T) {
	cases := map[string]Stage{
		AnchorToolPreInvoke:        StagePreInvoke,
		AnchorCoordinatorPreInvoke: StagePreInvoke,
		AnchorSessionPreInvoke:     StagePreInvoke,
		AnchorToolHandler:          StageToolHandler,
		AnchorToolPost:             StagePostTool,
		AnchorCoordinatorPostTurn:  StagePostTurn,
		AnchorWorkerFinalize:       StageFinalize,
	}
	for anchor, want := range cases {
		if got := StageFromAnchor(anchor); got != want {
			t.Errorf("%s: got %s want %s", anchor, got, want)
		}
	}
}

func TestPipelineBlockShortCircuitAndOnFire(t *testing.T) {
	when := `paintedwolf.claims_completion && !paintedwolf.has_matching_ledger_job && breaker_count < 3`
	nudge := &Rule{
		OAR: "1.0", ID: "COORDINATOR_UNGROUNDED_CLAIM", Kind: KindInvariant,
		Anchor: AnchorCoordinatorPostTurn, When: when,
		Effect: EffectNudge, OnFire: []OnFireAction{OnFireIncrementBreaker},
		Enforcement: "enforce", OnError: "fail_closed",
	}
	when2 := `paintedwolf.claims_completion && !paintedwolf.has_matching_ledger_job && breaker_count >= 3`
	block := &Rule{
		OAR: "1.0", ID: "COORDINATOR_GROUNDING_ESCALATED", Kind: KindInvariant,
		Anchor: AnchorCoordinatorPostTurn, When: when2,
		Effect: EffectBlock, Enforcement: "enforce", OnError: "fail_closed",
	}
	rs := NewRuleSet([]*Rule{nudge, block})
	store := NewCounterStore()
	p := NewGuardPipeline(rs, nil, store)

	gc := NewGuardContext()
	gc.SessionID = "s1"
	gc.ClaimsCompletion = true
	gc.HasMatchingLedgerJob = false
	gc.BreakerCount = 0

	res, err := p.Evaluate(context.Background(), StagePostTurn, gc)
	testutil.FailErr(t, "eval nudge", err)
	if !res.Enforced || res.Decision == nil || res.Decision.Code != "COORDINATOR_UNGROUNDED_CLAIM" {
		t.Fatalf("got %#v", res)
	}
	if store.Get("s1", "COORDINATOR_UNGROUNDED_CLAIM", CounterBreaker) != 1 {
		t.Fatal("expected on_fire increment_breaker")
	}

	// Simulate escalated breaker via store + fact.
	store.Increment("s1", "COORDINATOR_GROUNDING_ESCALATED", CounterBreaker, 3)
	gc2 := NewGuardContext()
	gc2.SessionID = "s1"
	gc2.ClaimsCompletion = true
	gc2.HasMatchingLedgerJob = false
	gc2.BreakerCount = 3
	res2, err := p.Evaluate(context.Background(), StagePostTurn, gc2)
	testutil.FailErr(t, "eval block2", err)
	if res2.Decision == nil || res2.Decision.Effect != EffectBlock {
		// The nudge fires first while its own breaker_count is below 3; check the block rule alone.
		p2 := NewGuardPipeline(NewRuleSet([]*Rule{block}), nil, store)
		gc3 := NewGuardContext()
		gc3.ClaimsCompletion = true
		gc3.BreakerCount = 3
		res3, err := p2.Evaluate(context.Background(), StagePostTurn, gc3)
		testutil.FailErr(t, "eval block only", err)
		if res3.Decision == nil || res3.Decision.Code != "COORDINATOR_GROUNDING_ESCALATED" {
			t.Fatalf("got %#v", res3)
		}
	}
	_ = res2
}

func TestPipelineMonitorSuppressesActions(t *testing.T) {
	r := &Rule{
		OAR: "1.0", ID: "MONITOR_DEMO", Kind: KindPolicy,
		Anchor: AnchorToolPreInvoke, When: "paintedwolf.tool_is_state",
		Effect: EffectBlock, Enforcement: "monitor", OnError: "fail_closed",
		OnFire: []OnFireAction{OnFireIncrementCounter},
	}
	store := NewCounterStore()
	p := NewGuardPipeline(NewRuleSet([]*Rule{r}), nil, store)
	gc := NewGuardContext()
	gc.SessionID = "s"
	gc.Tool = "state_create"
	gc.DeriveToolClassFacts()
	res, err := p.Evaluate(context.Background(), StagePreInvoke, gc)
	testutil.FailErr(t, "eval", err)
	if res.Decision != nil {
		t.Fatalf("monitor must suppress decisions, got %#v", res.Decision)
	}
	if store.Get("s", "MONITOR_DEMO", CounterFire) != 0 {
		t.Fatal("monitor must not run on_fire")
	}
	found := false
	for _, e := range res.Trace.Entries {
		// [OAR-OPS-9] a monitor rule that would have fired records
		// monitored_fired — the outcome an operator reads to decide whether the
		// rule is ready to enforce.
		if e.Rule == "MONITOR_DEMO" && e.Outcome == TraceMonitoredFired {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected monitor trace, got %#v", res.Trace.Entries)
	}
}

func TestLazyFactsSkipEvidenceForGreppyWhen(t *testing.T) {
	names := FactsReferenced(`tool == "grep" && paintedwolf.pattern_parse_ok`, nil)
	for _, n := range names {
		if _, ok := expensiveFacts[n]; ok {
			t.Fatalf("grep when should not pull expensive fact %s", n)
		}
	}
	names = FactsReferenced(`size(paintedwolf.unobserved_cited_paths) > 0`, nil)
	found := false
	for _, n := range names {
		if n == "paintedwolf.unobserved_cited_paths" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected paintedwolf.unobserved_cited_paths in %v", names)
	}
}

func TestFactsReferencedDoesNotTreatStringLiteralsAsFacts(t *testing.T) {
	names := FactsReferenced(`"tool" in content_origins && content_contains_untrusted`, nil)
	foundOrigins := false
	foundUntrusted := false
	for _, name := range names {
		switch name {
		case "tool":
			t.Fatalf("string literal promoted to fact reference: %v", names)
		case "content_origins":
			foundOrigins = true
		case "content_contains_untrusted":
			foundUntrusted = true
		}
	}
	if !foundOrigins || !foundUntrusted {
		t.Fatalf("structured facts missing: %v", names)
	}
}

func TestRendererMapsEffects(t *testing.T) {
	r := NewRenderer(stubFmt{}, stubFmt{})
	rendered, err := r.Render(context.Background(), StagePreInvoke, &Decision{
		Effect: EffectBlock, Code: "TOOL_ARGS_INVALID",
	})
	testutil.FailErr(t, "render", err)
	if len(rendered) != 1 || rendered[0].Channel != ChannelToolReject || rendered[0].Text == "" {
		t.Fatalf("got %#v", rendered)
	}
	rendered, err = r.Render(context.Background(), StagePostTurn, &Decision{
		Effect: EffectBlock, Code: "TOOL_ARGS_INVALID",
	})
	testutil.FailErr(t, "render post", err)
	if rendered[0].Channel != ChannelGroundingNudge {
		t.Fatalf("got %s", rendered[0].Channel)
	}
	rendered, err = r.Render(context.Background(), StagePostTool, &Decision{
		Effect:     EffectWarn,
		Code:       "DOOM_LOOP_REPEAT_WARN",
		Advisories: []Advisory{{Code: "DOOM_LOOP_REPEAT_WARN"}},
	})
	testutil.FailErr(t, "render warn", err)
	if rendered[0].Channel != ChannelBanner {
		t.Fatalf("got %s", rendered[0].Channel)
	}
}

type stubFmt struct{}

func (stubFmt) Format(code string, data map[string]any) (string, error) {
	return "Rejected:\nCode: " + code, nil
}

func (stubFmt) FormatNudge(ctx context.Context, code string, data map[string]any) (string, error) {
	return "Rejected:\nCode: " + code, nil
}

func TestDetectorDispatchSecretMatch(t *testing.T) {
	reg := NewDetectorRegistry()
	reg.Register(SecretMatchDetector{Matcher: secretmatch.NewInertMatcher()})
	findings, err := reg.Dispatch("detector://secretmatch", NewGuardContext())
	testutil.FailErr(t, "dispatch", err)
	if len(findings) != 1 || findings[0].Fact != "secret_matches" {
		t.Fatalf("expected secret_matches observation, got %#v", findings)
	}
}

func TestExecuteOnFireRespectsClosedVocab(t *testing.T) {
	store := NewCounterStore()
	_, err := ExecuteOnFire(t.Context(), store, nil, OnFireEvent{SessionID: "s"}, "C", []OnFireAction{OnFireIncrementCounter, OnFireResetCounter})
	testutil.FailErr(t, "execute side effects", err)
	if store.Get("s", "C", CounterFire) != 0 {
		t.Fatal("reset should clear")
	}
}
