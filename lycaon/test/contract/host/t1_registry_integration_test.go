package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestPostureDenyUsesRegisteredToolName(t *testing.T) {

	postures, err := session.LoadPostureRegistry()
	contractcheck.FailErr(t, "session.LoadPostureRegistry failed", err)
	packs, err := rules.LoadBundledRules()
	contractcheck.FailErr(t, "rules.LoadBundledRules failed", err)
	condReg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build conditions registry", err)
	if err := rules.RegisterRuleConditions(condReg); err != nil {
		contractcheck.FailErr(t, "register rule conditions", err)
	}
	engine, err := rules.NewPostureRuleEngine(postures, packs, condReg)
	contractcheck.FailErr(t, "rules.NewPostureRuleEngine failed", err)

	out, err := engine.Evaluate(context.Background(), rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureSpec, ToolName: "state_create"}})
	contractcheck.FailErr(t, "engine.Evaluate failed", err)
	if out.Allowed {
		t.Fatal("expected spec posture to deny state_create")
	}
	if out.Code == "" {
		t.Fatal("expected structured deny code")
	}

	allow, err := engine.Evaluate(context.Background(), rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureBuild, ToolName: "state_query"}})
	contractcheck.FailErr(t, "engine.Evaluate failed", err)
	if !allow.Allowed {
		t.Fatalf("expected state_query allowed in build posture, code=%q", allow.Code)
	}
}
