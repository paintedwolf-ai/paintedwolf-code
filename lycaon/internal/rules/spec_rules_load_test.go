package rules_test

import (
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/vocabulary"
)

func TestBundledSpecRulesLoad(t *testing.T) {
	cfg, err := rules.LoadRulesConfig(config.PostureRulesDir.Join("spec.yaml"))
	testutil.FailErr(t, "load rules config YAML", err)
	if len(cfg.Rules) < 20 {
		t.Fatalf("spec rules count = %d want >= 20", len(cfg.Rules))
	}
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "register rule conditions", err)
	}
	if diags := vocabulary.ValidateBundled(reg, nil, []*rules.RulesConfig{cfg}); len(diags) > 0 {
		t.Fatalf("vocabulary.ValidateBundled failed: %v", diags)
	}
}
