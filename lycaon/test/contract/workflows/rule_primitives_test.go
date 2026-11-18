package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/rules/rulestest"
	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func ruleConditionRegistry(t *testing.T) *conditions.ConditionRegistry {
	t.Helper()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build conditions registry", err)
	contractcheck.FailErr(t, "register rule conditions", rules.RegisterRuleConditions(reg))
	return reg
}

func TestRuleYAMLWhenKeysAreImplemented(t *testing.T) {
	reg := ruleConditionRegistry(t)
	packs, err := rules.LoadBundledRules()
	contractcheck.FailErr(t, "load bundled rules", err)
	for path, cfg := range packs {
		for _, rule := range cfg.Rules {
			if err := rules.ValidateWhen(reg, rule.When); err != nil {
				t.Errorf("%s rule %q: %v", path, rule.ID, err)
			}
			if raw, exists := rule.When["posture_is"]; exists {
				val, _ := raw.(string)
				if !sessionposture.ValidSessionPosture(val) {
					t.Errorf("%s rule %q: invalid posture_is %q", path, rule.ID, val)
				}
			}
		}
	}
}

func TestRuleEnginePostureBehaviorContract(t *testing.T) {
	t.Parallel()
	engine := rulestest.BundledEngine(t, ruleConditionRegistry(t))
	ctx := context.Background()

	deny, err := engine.Evaluate(ctx, rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureSpec, ToolName: "plan_from_blueprint"}})
	contractcheck.FailErr(t, "engine.Evaluate failed", err)
	if deny.Allowed {
		t.Fatal("expected plan_from_blueprint denied in spec posture")
	}
	if deny.Code != "SPEC_POSTURE_DELEGATION_FORBIDDEN" {
		t.Fatalf("code = %q", deny.Code)
	}

	allow, err := engine.Evaluate(ctx, rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureBuild, ToolName: "plan_from_blueprint"}})
	contractcheck.FailErr(t, "engine.Evaluate failed", err)
	if !allow.Allowed {
		t.Fatalf("expected plan_from_blueprint allowed in build posture, got %q", allow.Code)
	}
}
