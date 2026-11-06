package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	runstate "github.com/lycaon/lycaon/internal/workflow/runstate"
)

func TestSecuritySurveyPlanRequiresThreatModel(t *testing.T) {
	t.Parallel()
	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	m, err := manifests.Get("security-survey", "2.0.1")
	testutil.FailErr(t, "manifests.Get", err)
	plan, ok := m.PhaseByID("plan")
	if !ok {
		t.Fatal("security-survey plan phase missing")
	}
	if !plan.Fanout.RequireThreatModel {
		t.Fatal("security-survey plan must set fanout.require_threat_model")
	}
	claims, ok := m.PhaseByID("claims")
	if !ok || claims.ReviewLoop == nil {
		t.Fatal("security-survey claims review_loop missing")
	}
	for _, key := range []string{"verdict", "threat_model", "claims"} {
		if _, ok := claims.ReviewLoop.VerdictSchema[key]; !ok {
			t.Fatalf("claims verdict_schema missing %q", key)
		}
	}
	if len(claims.ReviewLoop.RequiredAgents) != 0 {
		t.Fatalf("claims required_agents = %v want empty", claims.ReviewLoop.RequiredAgents)
	}

	challenge, ok := m.PhaseByID("challenge")
	if !ok || challenge.ReviewLoop == nil {
		t.Fatal("security-survey challenge review_loop missing")
	}
	if challenge.ReviewLoop.VerdictSchema["verdict"] != "CHALLENGED|NEEDS_INVESTIGATION" {
		t.Fatalf("challenge verdict_schema = %+v", challenge.ReviewLoop.VerdictSchema)
	}
	if len(challenge.ReviewLoop.RequiredAgents) != 1 || challenge.ReviewLoop.RequiredAgents[0] != "skeptic" {
		t.Fatalf("challenge required_agents = %v want [skeptic]", challenge.ReviewLoop.RequiredAgents)
	}
	if len(challenge.ReviewLoop.IfSpawnable) != 1 || challenge.ReviewLoop.IfSpawnable[0] != "web-researcher" {
		t.Fatalf("challenge if_spawnable = %v want [web-researcher]", challenge.ReviewLoop.IfSpawnable)
	}
	excluded := runstate.ReviewLoopFanoutExcludedAgents(m)
	if len(excluded) != 2 || excluded[0] != "skeptic" || excluded[1] != "web-researcher" {
		t.Fatalf("fanout excluded reviewers = %v want [skeptic web-researcher]", excluded)
	}
}
