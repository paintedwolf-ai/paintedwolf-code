package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	runstate "github.com/lycaon/lycaon/internal/workflow/runstate"
)

func TestSecuritySurveyPlanRequiresThreatModel(t *testing.T) {
	t.Parallel()
	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	m, err := manifests.Get("security-survey", "1.0.1")
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

func TestSecuritySurveyPromptsGroundAgainstThreatModel(t *testing.T) {
	t.Parallel()
	root := filepath.Join(configlayout.FindModuleRoot(), "config", "packs", "painted-wolf")
	synthesis, err := os.ReadFile(filepath.Join(root, "security-survey", "guidance", "coordinator-security-synthesis.md"))
	testutil.FailErr(t, "read security synthesis", err)
	got := string(synthesis)
	for _, needle := range []string{"threat model", "empty", "coverage"} {
		if !strings.Contains(strings.ToLower(got), needle) {
			t.Fatalf("coordinator-security-synthesis.md missing %q", needle)
		}
	}

	shared, err := os.ReadFile(filepath.Join(root, "platform", "guidance", "coordinator-topology-synthesis.md"))
	testutil.FailErr(t, "read topology synthesis", err)
	if strings.Contains(string(shared), "severity") {
		t.Fatal("coordinator-topology-synthesis.md mentions severity")
	}

	plan, err := os.ReadFile(filepath.Join(root, "security-survey", "guidance", "coordinator-security-plan.md"))
	testutil.FailErr(t, "read security plan", err)
	if !strings.Contains(string(plan), "threat_model") {
		t.Fatal("coordinator-security-plan.md missing threat_model")
	}

	claims, err := os.ReadFile(filepath.Join(root, "security-survey", "guidance", "coordinator-security-claims.md"))
	testutil.FailErr(t, "read security claims", err)
	claimsText := string(claims)
	if !strings.Contains(claimsText, "submit_verdict") || !strings.Contains(claimsText, "CLAIMED") {
		t.Fatal("coordinator-security-claims.md missing CLAIMED submit_verdict")
	}
	if strings.Contains(strings.ToLower(claimsText), "from a security standpoint") {
		t.Fatal("coordinator-security-claims.md must not wrap the user ask as a skeptic proposition")
	}

	challenge, err := os.ReadFile(filepath.Join(root, "security-survey", "guidance", "coordinator-security-challenge.md"))
	testutil.FailErr(t, "read security challenge", err)
	challengeText := string(challenge)
	for _, needle := range []string{"stamped claims", "survives", "web-researcher", "spawnable_reviewers"} {
		if !strings.Contains(strings.ToLower(challengeText), needle) {
			t.Fatalf("coordinator-security-challenge.md missing %q", needle)
		}
	}
	if strings.Contains(strings.ToLower(challengeText), "from a security standpoint") {
		t.Fatal("coordinator-security-challenge.md must not invert the user ask")
	}
}
