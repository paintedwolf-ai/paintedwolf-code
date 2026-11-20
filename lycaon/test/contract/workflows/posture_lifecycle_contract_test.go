package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestPostureRuleBehaviorMatrix(t *testing.T) {
	postures, err := profiles.LoadPostureRegistry()
	contractcheck.FailErr(t, "profiles.LoadPostureRegistry failed", err)
	packs, err := rules.LoadBundledRules()
	contractcheck.FailErr(t, "rules.LoadBundledRules failed", err)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build conditions registry", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		contractcheck.FailErr(t, "register rule conditions", err)
	}
	engine, err := rules.NewPostureRuleEngine(postures, packs, reg)
	contractcheck.FailErr(t, "rules.NewPostureRuleEngine failed", err)

	cases := []struct {
		name     string
		posture  api.SessionPosture
		tool     string
		args     map[string]any
		allowed  []string
		wantDeny bool
		wantCode string
	}{
		{"spec/delegate", api.SessionPostureSpec, "plan_from_blueprint", nil, nil, true, "SPEC_POSTURE_DELEGATION_FORBIDDEN"},
		{"spec/state", api.SessionPostureSpec, "state_create", nil, nil, true, "SPEC_POSTURE_STATE_FORBIDDEN"},
		{"build/delegate", api.SessionPostureBuild, "plan_from_blueprint", nil, nil, false, ""},
		{"build/disallowed-agent", api.SessionPostureBuild, "task", map[string]any{"agent_type": "sentinel"}, []string{"implementer"}, true, "DISALLOWED_AGENT"},
		{"vet/delegate", api.SessionPostureVet, "plan_from_blueprint", nil, nil, true, "VET_POSTURE_DELEGATION_FORBIDDEN"},
		// A vet turn resolves to an observe_ surface that offers no write.
		{"vet/write", api.SessionPostureVet, "write", nil, nil, false, ""},
		{"spec/write", api.SessionPostureSpec, "write", nil, nil, false, ""},
	}

	ctx := context.Background()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := engine.Evaluate(ctx, rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: tc.posture, ToolName: tc.tool, ToolArgs: tc.args, AllowedAgents: tc.allowed}})
			contractcheck.FailErr(t, "engine.Evaluate failed", err)
			if tc.wantDeny {
				if out.Allowed {
					t.Fatal("expected deny")
				}
				if out.Code != tc.wantCode {
					t.Fatalf("code = %q want %q", out.Code, tc.wantCode)
				}
				return
			}
			if !out.Allowed {
				t.Fatalf("expected allow, got deny %q", out.Code)
			}
		})
	}
}
