package surface

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestLoadCoordinatorFlow_shippedTableValid(t *testing.T) {
	table, err := LoadCoordinatorFlow()
	testutil.FailErr(t, "LoadCoordinatorFlow", err)
	if table.HitPolicy != "first" {
		t.Fatalf("hit_policy = %q", table.HitPolicy)
	}
	if len(table.Rules) < 10 {
		t.Fatalf("rules = %d want full table", len(table.Rules))
	}
}

func TestLoadCoordinatorFlow_bundledTableEvaluates(t *testing.T) {
	table, err := LoadCoordinatorFlow()
	testutil.FailErr(t, "LoadCoordinatorFlow", err)
	if len(table.Rules) == 0 {
		t.Fatal("bundled table has no rules")
	}
	ev, err := EvaluateFlow(table, SurfaceFacts{HasComposeDraft: true})
	testutil.FailErr(t, "EvaluateFlow", err)
	if ev.SurfaceID != "workflow_compose" {
		t.Fatalf("surface = %q", ev.SurfaceID)
	}
}

func TestValidateFlowTable_rejectsUnknownSurface(t *testing.T) {
	table := FlowTable{
		HitPolicy: "first",
		Rules: []FlowRule{{
			When: []FlowCondition{{Fact: FactHasComposeDraft, Comparator: FlowCmpEq, EqBool: true}},
			Out:  FlowOutput{SurfaceID: "not_a_real_surface"},
		}},
		Default: FlowOutput{SurfaceID: "implement_synthesis"},
	}
	err := ValidateFlowTable(table)
	if err == nil || !strings.Contains(err.Error(), "unknown surface") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateFlowTable_rejectsUnknownFact(t *testing.T) {
	table := FlowTable{
		HitPolicy: "first",
		Rules: []FlowRule{{
			When: []FlowCondition{{Fact: "mystery_fact", Comparator: FlowCmpEq, EqBool: true}},
			Out:  FlowOutput{SurfaceID: "implement_synthesis"},
		}},
		Default: FlowOutput{SurfaceID: "implement_synthesis"},
	}
	err := ValidateFlowTable(table)
	if err == nil || !strings.Contains(err.Error(), "unknown fact") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseFlowCondition_rejectsBadComparator(t *testing.T) {
	_, err := parseFlowCondition(FactWorkersInFlight, 3.14)
	if err == nil {
		t.Fatal("expected unsupported comparator error")
	}
}

func TestValidateFlowTable_rejectsMissingDefault(t *testing.T) {
	table := FlowTable{HitPolicy: "first", Default: FlowOutput{}}
	err := ValidateFlowTable(table)
	if err == nil || !strings.Contains(err.Error(), "default surface") {
		t.Fatalf("err = %v", err)
	}
}

type ruleReachabilityCase struct {
	name     string
	facts    SurfaceFacts
	wantSurf string
	wantRule int
}

var ruleReachabilityCases = []ruleReachabilityCase{
	{
		name:     "compose_draft",
		facts:    SurfaceFacts{HasComposeDraft: true},
		wantSurf: "workflow_compose",
		wantRule: 0,
	},
	{
		name:     "overlay_promote",
		facts:    SurfaceFacts{OverlayPromotePending: 2},
		wantSurf: SurfaceImplementOverlayPromote,
		wantRule: 1,
	},
	{
		name:     "host_held_person_turn",
		facts:    SurfaceFacts{PhaseHostHeld: true, VisibleUserTurn: true, ManifestBoundSurface: "observe_investigate"},
		wantSurf: toolcontract.SurfaceAwaitHost,
		wantRule: 2,
	},
	{
		name:     "host_held_host_turn_keeps_manifest_surface",
		facts:    SurfaceFacts{PhaseHostHeld: true, HostCycleTurn: true, ManifestBoundSurface: "observe_investigate"},
		wantSurf: "observe_investigate",
		wantRule: 3,
	},
	{
		name:     "manifest_bound",
		facts:    SurfaceFacts{ManifestBoundSurface: "plan_research"},
		wantSurf: "plan_research",
		wantRule: 3,
	},
	{
		name:     "child_subroutine_visible_user",
		facts:    SurfaceFacts{ChildSubroutineBlocksInvestigate: true, VisibleUserTurn: true},
		wantSurf: SurfaceImplementDispatch,
		wantRule: 4,
	},
	{
		name:     "declared_orchestrate_visible_user",
		facts:    SurfaceFacts{WorkflowDeclaredMode: ExecutionModeFamilyOrchestrate, VisibleUserTurn: true},
		wantSurf: SurfaceImplementDispatch,
		wantRule: 5,
	},
	{
		// A later call of the same request, after a refused dispatch, keeps `task`.
		name:     "declared_orchestrate_continuation_before_dispatch",
		facts:    SurfaceFacts{WorkflowDeclaredMode: ExecutionModeFamilyOrchestrate},
		wantSurf: SurfaceImplementDispatch,
		wantRule: 5,
	},
	{
		name:     "declared_orchestrate_workers_park",
		facts:    SurfaceFacts{WorkflowDeclaredMode: ExecutionModeFamilyOrchestrate, WorkersInFlight: 1},
		wantSurf: SurfaceImplementPark,
		wantRule: 6,
	},
	{
		name:     "declared_investigate",
		facts:    SurfaceFacts{WorkflowDeclaredMode: ExecutionModeFamilyInvestigate, InvestigateHardBlock: false},
		wantSurf: toolcontract.SurfaceImplementInvestigate,
		wantRule: 7,
	},
	{
		name:     "investigate_default_eligible",
		facts:    SurfaceFacts{InvestigateDefaultEligible: true, InvestigateHardBlock: false},
		wantSurf: toolcontract.SurfaceImplementInvestigate,
		wantRule: 8,
	},
	{
		name:     "worker_finished_dispatch",
		facts:    SurfaceFacts{WorkerTaskFinishedTurn: true, WorkersInFlight: 2},
		wantSurf: SurfaceImplementDispatch,
		wantRule: 9,
	},
	{
		name:     "workers_park",
		facts:    SurfaceFacts{WorkersInFlight: 1},
		wantSurf: SurfaceImplementPark,
		wantRule: 10,
	},
	{
		name:     "open_repair",
		facts:    SurfaceFacts{WrapupGatesLoaded: true, OpenRepairSinceUserIntent: true},
		wantSurf: toolcontract.SurfaceImplementInvestigate,
		wantRule: 11,
	},
	{
		name: "plan_missing_after_dispatch",
		facts: SurfaceFacts{
			WrapupGatesLoaded:                     true,
			BatchReadyForSynthesis:                true,
			ProgressMissing:                       true,
			ProgressGatedToolAttemptedSinceIntent: true,
		},
		wantSurf: toolcontract.SurfaceImplementInvestigate,
		wantRule: 12,
	},
	{
		name:     "batch_ready",
		facts:    SurfaceFacts{WrapupGatesLoaded: true, BatchReadyForSynthesis: true},
		wantSurf: "implement_synthesis",
		wantRule: 13,
	},
	{
		// A returned batch with plan steps still open: the next wave stays dispatchable.
		name:     "host_cycle_open_plan_orchestrate_declared",
		facts:    SurfaceFacts{HostCycleTurn: true, ProgressHasOpenSteps: true, WorkflowDeclaredMode: ExecutionModeFamilyOrchestrate, BatchReadyForSynthesis: true},
		wantSurf: SurfaceImplementDispatch,
		wantRule: 14,
	},
	{
		name:     "host_cycle_open_plan_read_scout",
		facts:    SurfaceFacts{HostCycleTurn: true, ProgressHasOpenSteps: true},
		wantSurf: toolcontract.SurfaceImplementInvestigate,
		wantRule: 15,
	},
	{
		name:     "host_cycle",
		facts:    SurfaceFacts{HostCycleTurn: true},
		wantSurf: toolcontract.SurfaceImplementInvestigate,
		wantRule: 15,
	},
	{
		name:     "visible_user_investigate",
		facts:    SurfaceFacts{VisibleUserTurn: true, InvestigateDefaultEligible: true, InvestigateHardBlock: false},
		wantSurf: toolcontract.SurfaceImplementInvestigate,
		wantRule: 8,
	},
	{
		name:     "default_investigate",
		facts:    SurfaceFacts{},
		wantSurf: toolcontract.SurfaceImplementInvestigate,
		wantRule: -1,
	},
}

func TestEvaluateFlow_eachRuleReachable(t *testing.T) {
	table, err := LoadCoordinatorFlow()
	testutil.FailErr(t, "LoadCoordinatorFlow", err)

	for _, tc := range ruleReachabilityCases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := EvaluateFlow(table, tc.facts)
			testutil.FailErr(t, "EvaluateFlow", err)
			if ev.SurfaceID != tc.wantSurf {
				t.Fatalf("surface = %q want %q (rule %d)", ev.SurfaceID, tc.wantSurf, ev.MatchedRuleIdx)
			}
			if ev.MatchedRuleIdx != tc.wantRule {
				t.Fatalf("matched rule = %d want %d", ev.MatchedRuleIdx, tc.wantRule)
			}
		})
	}
}

func TestEvaluateFlow_altTableChangesPersonality(t *testing.T) {
	alt := FlowTable{
		HitPolicy: "first",
		Rules: []FlowRule{{
			When: []FlowCondition{{Fact: FactInvestigateDefaultEligible, Comparator: FlowCmpEq, EqBool: true}},
			Out:  FlowOutput{SurfaceID: "implement_routing"},
		}},
		Default: FlowOutput{SurfaceID: toolcontract.SurfaceImplementInvestigate},
	}
	facts := SurfaceFacts{InvestigateDefaultEligible: true, InvestigateHardBlock: false}
	ev, err := EvaluateFlow(alt, facts)
	testutil.FailErr(t, "EvaluateFlow", err)
	if ev.SurfaceID != "implement_routing" {
		t.Fatalf("alt table surface = %q want implement_routing", ev.SurfaceID)
	}

	shipped, err := ShippedCoordinatorFlowTable()
	testutil.FailErr(t, "ShippedCoordinatorFlowTable", err)
	evShipped, err := EvaluateFlow(shipped, facts)
	testutil.FailErr(t, "EvaluateFlow shipped", err)
	if evShipped.SurfaceID == ev.SurfaceID {
		t.Fatal("shipped and alt tables must diverge on the same facts")
	}
}

func TestCoordinatorSurfaceModeRef_catalogSurfaces(t *testing.T) {
	for surfaceID, want := range map[string]string{
		toolcontract.SurfaceImplementInvestigate: "implement-investigate",
		"implement_routing":                      "implement-routing",
		SurfaceImplementPark:                     "implement-park",
		"review_adjudicate":                      "review-adjudicate",
	} {
		got, err := CoordinatorSurfaceModeRef(surfaceID)
		testutil.FailErr(t, "CoordinatorSurfaceModeRef", err)
		if got != want {
			t.Fatalf("surface %q mode_ref = %q want %q", surfaceID, got, want)
		}
	}
	if _, err := CoordinatorSurfaceModeRef("not_a_surface"); err == nil {
		t.Fatal("unknown surface must fail closed")
	}
}

func TestEveryCoordinatorSurfaceDeclaresResolvableMode(t *testing.T) {
	catalog, err := surfacecatalog.Load()
	testutil.FailErr(t, "load coordinator surfaces", err)
	layout := prompts.DefaultBundledLayout()
	for _, id := range catalog.SurfaceIDs() {
		ref, err := CoordinatorSurfaceModeRef(id)
		testutil.FailErr(t, "mode_ref for "+id, err)
		if _, _, err := layout.ReadBundled(ModeTemplateRef(ref)); err != nil {
			t.Fatalf("surface %q mode_ref %q does not resolve: %v", id, ref, err)
		}
	}
}

func TestReviewAdjudicateUsesVerdictModeContract(t *testing.T) {
	profile := ResolveTurnProfileForSurface("review_adjudicate", api.CoordinatorRunContext{}, nil)
	if len(profile.ModeRefs) != 1 || profile.ModeRefs[0] != "review-adjudicate" {
		t.Fatalf("review profile mode_refs = %v", profile.ModeRefs)
	}
	body, _, err := prompts.DefaultBundledLayout().ReadBundled(ModeTemplateRef(profile.ModeRefs[0]))
	testutil.FailErr(t, "read review adjudicate mode", err)
	text := string(body)
	for _, want := range []string{"`cited_evidence`", "`handle`", "`submit_verdict`'s schema", "Claims and coverage assessments also carry their own", "Do not use an `evidence` key"} {
		if !strings.Contains(text, want) {
			t.Fatalf("review adjudicate mode missing %q", want)
		}
	}
	if strings.Contains(text, "## Synthesis turn") {
		t.Fatal("review adjudicate must not receive synthesis mode copy")
	}
}

// The shipped accessor and bundled flow definition share one source.
func TestShippedCoordinatorFlowIsBundledYAML(t *testing.T) {
	raw, err := config.Read(config.CoordinatorFlow)
	testutil.FailErr(t, "read bundled coordinator flow", err)
	fromYAML, err := parseFlowTableYAML(raw)
	testutil.FailErr(t, "parse bundled coordinator flow", err)
	shipped, err := ShippedCoordinatorFlowTable()
	testutil.FailErr(t, "ShippedCoordinatorFlowTable", err)
	if len(fromYAML.Rules) != len(shipped.Rules) {
		t.Fatalf("rule count bundled=%d shipped=%d", len(fromYAML.Rules), len(shipped.Rules))
	}
	if fromYAML.Default.SurfaceID != shipped.Default.SurfaceID {
		t.Fatalf("default surface diverges bundled=%q shipped=%q", fromYAML.Default.SurfaceID, shipped.Default.SurfaceID)
	}
}

func TestParseFlowTableYAML_rejectsInvalidYAML(t *testing.T) {
	_, err := parseFlowTableYAML([]byte("hit_policy: first\nrules: [{when: {bad: [1,2]}}]"))
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestEvaluateFlow_usesCatalogModeRef(t *testing.T) {
	table := FlowTable{
		HitPolicy: "first",
		Rules: []FlowRule{{
			When: []FlowCondition{{Fact: FactHasComposeDraft, Comparator: FlowCmpEq, EqBool: true}},
			Out:  FlowOutput{SurfaceID: "workflow_compose"},
		}},
		Default: FlowOutput{SurfaceID: "implement_synthesis"},
	}
	ev, err := EvaluateFlow(table, SurfaceFacts{HasComposeDraft: true})
	testutil.FailErr(t, "EvaluateFlow", err)
	if ev.BaseModeRef != "compose" {
		t.Fatalf("base_mode_ref = %q", ev.BaseModeRef)
	}
}

func TestReadCoordinatorFlowBytes_emptyFileFailsClosed(t *testing.T) {
	configtest.Overlay(t, map[config.Rel]string{config.CoordinatorFlow: "   \n"})
	_, err := readCoordinatorFlowBytes()
	if err == nil {
		t.Fatal("expected error for whitespace-only coordinator-flow.yaml")
	}
}
