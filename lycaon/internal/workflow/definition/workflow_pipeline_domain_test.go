package definition_test

import (
	"regexp"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestBugbashManifestVocabularyRegistered(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "register rule conditions", err)
	}
	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs failed", err)
	manifest, err := manifests.Get("bugbash", "1.0.0")
	testutil.FailErr(t, "manifests.Get failed", err)
	forbiddenStageComplete := regexp.MustCompile(`stage_[a-z0-9_]+_complete`)
	for _, p := range manifest.PhaseDefs {
		if cw := p.CompleteWhen; cw != "" {
			if !workflowdef.IsKnownCompleteWhen(cw) {
				t.Fatalf("phase %q unknown complete_when %q", p.ID, cw)
			}
			if forbiddenStageComplete.MatchString(cw) {
				t.Fatalf("phase %q uses forbidden stage_*_complete: %q", p.ID, cw)
			}
			if conditions.IsCatalogStub(cw) {
				t.Fatalf("phase %q uses catalog-only id %q", p.ID, cw)
			}
		}
	}
	orchCfg, err := rules.LoadRulesConfig(config.PostureRulesDir.Join("orchestrate.yaml"))
	testutil.FailErr(t, "load rules config YAML", err)
	for _, rule := range orchCfg.Rules {
		whenStr, err := rules.CanonicalWhenString(rule.When)
		if err != nil {
			t.Fatalf("rule %q: %v", rule.ID, err)
		}
		if _, err := rules.MatchWhenExpr(reg, whenStr, rules.EvalContext{}); err != nil {
			t.Fatalf("rule %q when %q: %v", rule.ID, whenStr, err)
		}
	}
}
