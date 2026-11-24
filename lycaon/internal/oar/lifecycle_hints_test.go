package oar_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/testutil"
)

var lifecycleHintCodes = []string{
	"COORDINATOR_APPROVAL_SEAL_FAILED",
	"WORKFLOW_COMPOSE_SUCCEEDED",
	"COORDINATOR_EDIT_FOLLOWUP_REPEATED",
	"WORKFLOW_FEEDBACK_CARD_PENDING",
	"WORKFLOW_FEEDBACK_ANSWER_RECEIVED",
	"WORKFLOW_REVIEW_ROUND_CONTINUES",
	"WORKFLOW_REVIEW_CAP_REACHED",
	"COORDINATOR_SPEND_WIND_DOWN",
}

func TestLifecycleHintsLoadThroughProductionPackLoader(t *testing.T) {
	ctx := context.Background()
	catalog, err := extpacks.ResolveStockCatalog(ctx, nil)
	testutil.FailErr(t, "resolve stock catalog", err)
	view, err := catalogview.Build(ctx, configlayout.FindModuleRoot(), catalog)
	testutil.FailErr(t, "build catalog view", err)

	seen := map[string]bool{}
	for _, code := range lifecycleHintCodes {
		h, ok := view.Rules.Hint(code)
		if !ok || h == nil {
			t.Fatalf("missing loaded OAR hint %s", code)
		}
		if seen[h.ID] {
			t.Fatalf("duplicate hint id %s", h.ID)
		}
		seen[h.ID] = true
		msg, ok := view.Rules.HintMessage(code)
		if !ok || msg == "" {
			t.Fatalf("hint %s has empty message", code)
		}
		rendered, found := view.Rules.RenderHint(code)
		if !found || rendered != msg {
			t.Fatalf("RenderHint(%s) = (%q, %v), want (%q, true)", code, rendered, found, msg)
		}
		// Hints are never evaluated as guard rules.
		if _, isRule := view.Rules.Get(code); isRule {
			t.Fatalf("hint %s must not be indexed as a guard rule", code)
		}
	}

	// Retain WORKFLOW_FEEDBACK_PENDING rule beside WORKFLOW_FEEDBACK_CARD_PENDING hint.
	if _, ok := view.Rules.Get("WORKFLOW_FEEDBACK_PENDING"); !ok {
		t.Fatal("WORKFLOW_FEEDBACK_PENDING rule must remain loaded")
	}

	// OAR-HINT-4: unregistered raised code renders as the bare code with found=false.
	bare, found := view.Rules.RenderHint("UNREGISTERED_LIFECYCLE_HINT")
	if found || bare != "UNREGISTERED_LIFECYCLE_HINT" {
		t.Fatalf("RenderHint(unregistered) = (%q, %v), want (bare code, false)", bare, found)
	}
}

func TestLifecycleHintsRejectBindingsSideEffectsAndDuplicates(t *testing.T) {
	schemaDir := configlayout.SchemasDir(configlayout.FindModuleRoot())
	testutil.FailErr(t, "install engine declaration", oar.InstallEngineDeclarationBundled(schemaDir))
	loader, err := oar.NewLoader(schemaDir)
	testutil.FailErr(t, "new oar loader", err)

	rejected := []struct {
		name string
		raw  string
	}{
		{
			name: "pongo_interpolation_binding",
			raw:  `{"spec_version":"1.0","id":"BAD_BINDING_HINT","kind":"hint","message":"See {{ paintedwolf.phase }}"}`,
		},
		{
			name: "pongo_tag_binding",
			raw:  `{"spec_version":"1.0","id":"BAD_TAG_HINT","kind":"hint","message":"{% if true %}hi{% endif %}"}`,
		},
		{
			name: "side_effect_on_fire",
			raw:  `{"spec_version":"1.0","id":"BAD_EFFECT_HINT","kind":"hint","on_fire":["increment_counter"],"message":"Static copy."}`,
		},
		{
			name: "rule_anchor_and_effect",
			raw:  `{"spec_version":"1.0","id":"BAD_RULE_FIELD_HINT","kind":"hint","on":"tool.pre_invoke","effect":"block","message":"Static copy."}`,
		},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := loader.LoadHintJSON(json.RawMessage(tc.raw)); err == nil {
				t.Fatalf("%s: expected load rejection, got nil", tc.name)
			}
		})
	}

	// Duplicate hint IDs must be rejected by CheckRuleSet / MergeTiered.
	h1, err := loader.LoadHintJSON(json.RawMessage(`{"spec_version":"1.0","id":"DUP_HINT","kind":"hint","message":"First."}`))
	testutil.FailErr(t, "load first hint", err)
	h2, err := loader.LoadHintJSON(json.RawMessage(`{"spec_version":"1.0","id":"DUP_HINT","kind":"hint","message":"Second."}`))
	testutil.FailErr(t, "load second hint", err)
	if err := oarcore.CheckRuleSet(nil, []*oarcore.Hint{h1, h2}); err == nil {
		t.Fatal("expected duplicate hint ID rejection from CheckRuleSet")
	}
}

func TestLifecycleHintsDoNotChangeGuardDecisions(t *testing.T) {
	ctx := context.Background()
	catalog, err := extpacks.ResolveStockCatalog(ctx, nil)
	testutil.FailErr(t, "resolve stock catalog", err)
	view, err := catalogview.Build(ctx, configlayout.FindModuleRoot(), catalog)
	testutil.FailErr(t, "build catalog view", err)

	schemaDir := configlayout.SchemasDir(configlayout.FindModuleRoot())
	loader, err := oar.NewLoader(schemaDir)
	testutil.FailErr(t, "new oar loader", err)

	// Build a rule set with identical rules and no hints to prove guard equivalence.
	withoutHints := oar.NewRuleSet(view.Rules.All())
	if len(view.Rules.Hints()) < len(lifecycleHintCodes) {
		t.Fatalf("loaded hints = %d, want at least %d", len(view.Rules.Hints()), len(lifecycleHintCodes))
	}
	if len(withoutHints.Hints()) != 0 {
		t.Fatalf("withoutHints should have 0 hints, got %d", len(withoutHints.Hints()))
	}

	pWith := oar.NewGuardPipeline(view.Rules, loader, oar.NewCounterStore())
	pWithout := oar.NewGuardPipeline(withoutHints, loader, oar.NewCounterStore())
	for _, p := range []*oar.GuardPipeline{pWith, pWithout} {
		p.EnableAnchor(oar.AnchorToolRejected)
		p.EnableAnchor(oar.AnchorCoordinatorPostTurn)
	}

	gc := oar.NewGuardContext()
	gc.Tool = "workflow_transition"
	gc.Rejection.Code = "WORKFLOW_FEEDBACK_PENDING"

	dWith, err := pWith.Evaluate(oar.AnchorToolRejected, gc)
	testutil.FailErr(t, "evaluate with hints", err)
	dWithout, err := pWithout.Evaluate(oar.AnchorToolRejected, gc)
	testutil.FailErr(t, "evaluate without hints", err)
	if (dWith == nil) != (dWithout == nil) {
		t.Fatalf("decision presence mismatch: with=%v without=%v", dWith, dWithout)
	}
	if dWith != nil && (dWith.Code != dWithout.Code || dWith.Effect != dWithout.Effect || dWith.Rule != dWithout.Rule) {
		t.Fatalf("decision mismatch: with=%+v without=%+v", dWith, dWithout)
	}
	if !slices.EqualFunc(view.Rules.All(), withoutHints.All(), func(a, b *oar.Rule) bool { return a.Qualified() == b.Qualified() }) {
		t.Fatal("rule evaluation order differed between rule sets with and without hints")
	}
}
