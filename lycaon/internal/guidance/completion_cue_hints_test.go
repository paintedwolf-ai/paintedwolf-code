package guidance_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/completioncue"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPlanCompletionHintsLoadThroughOARPackLoader(t *testing.T) {
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load stock hint config", err)

	catalog, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "load effective pack catalog", err)

	loader, err := oar.NewLoader("")
	testutil.FailErr(t, "create OAR loader", err)

	ruleSetWithHints, err := loader.LoadEffectivePolicyWithCatalog(catalog)
	testutil.FailErr(t, "load OAR rules with hints", err)

	for _, code := range []string{completioncue.CodeOutcomeCoverage, completioncue.CodeFinalItemCoverage} {
		entry, ok := cfg.HintCodes[code]
		if !ok {
			t.Fatalf("stock hint registry missing %s", code)
		}
		if entry.SpecVersion != "1.0" || entry.Kind != "hint" || entry.ID != code {
			t.Fatalf("%s metadata mismatch: spec=%q kind=%q id=%q", code, entry.SpecVersion, entry.Kind, entry.ID)
		}
		if entry.Message == "" {
			t.Fatalf("%s missing static message", code)
		}
		if entry.What != "" || entry.Cause != "" || entry.Why != "" || entry.Fix != "" || entry.Instead != "" {
			t.Fatalf("%s must carry message as its sole copy field", code)
		}
		if got := cfg.ResolveHintMessage(code); got != entry.Message {
			t.Fatalf("ResolveHintMessage(%s) = %q, want %q", code, got, entry.Message)
		}
		for _, rule := range ruleSetWithHints.All() {
			if rule.ID == code {
				t.Fatalf("hint %s must never load as an evaluated OAR rule (OAR-HINT-1/6)", code)
			}
		}
	}

	// OAR-HINT-4: unregistered raised code delivers the bare code.
	const unregistered = "PLAN_UNREGISTERED_HINT_CODE"
	if got := cfg.ResolveHintMessage(unregistered); got != unregistered {
		t.Fatalf("ResolveHintMessage(unregistered) = %q, want bare code %q", got, unregistered)
	}
	var nilCfg *guidance.HintConfig
	if got := nilCfg.ResolveHintMessage(unregistered); got != unregistered {
		t.Fatalf("nil HintConfig ResolveHintMessage = %q, want bare code %q", got, unregistered)
	}
}
