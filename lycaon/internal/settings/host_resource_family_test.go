package settings

import (
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
)

func TestHostResourceFamilyPolicyDenyWins(t *testing.T) {
	t.Parallel()

	rules := []ApprovalRule{
		{Category: ApprovalCategoryHostResource, Pattern: "docker", Effect: ApprovalEffectAsk},
		{Category: ApprovalCategoryHostResource, Pattern: "containers.local", Effect: ApprovalEffectDeny},
	}
	effect, rule, matched := EvaluateHostResourceSubjects(
		rules,
		[]string{"docker", "containers.local"},
	)
	if !matched || effect != ApprovalEffectDeny || rule.Pattern != "containers.local" {
		t.Fatalf("effect=%q rule=%+v matched=%t", effect, rule, matched)
	}
}

func TestHostResourceFamilyAskDoesNotChangeConcreteGrantSubject(t *testing.T) {
	t.Parallel()

	rules := []ApprovalRule{{
		Category: ApprovalCategoryHostResource, Pattern: "containers.local", Effect: ApprovalEffectAsk,
	}}
	_, rule, matched := EvaluateHostResourceSubjects(rules, []string{"docker", "containers.local"})
	if !matched || rule.Pattern != "containers.local" {
		t.Fatalf("rule=%+v matched=%t", rule, matched)
	}
	predicate := hostResourceGrantPredicate(hitl.ProposedAction{
		HostResources: []string{"docker"}, HostResourceFamilies: []string{"containers.local"},
	})
	if predicate.Pattern != "docker" {
		t.Fatalf("grant pattern = %q, want concrete id", predicate.Pattern)
	}
}

func TestExactHostResourceRuleIsIndependentOfBroaderEffectiveDeny(t *testing.T) {
	rules := []ApprovalRule{
		{Category: ApprovalCategoryHostResource, Pattern: "docker", Effect: ApprovalEffectAsk},
		{Category: ApprovalCategoryHostResource, Pattern: "containers.local", Effect: ApprovalEffectDeny},
	}
	effect, _, matched := EvaluateHostResourceSubjects(rules, []string{"docker", "containers.local"})
	if !matched || effect != ApprovalEffectDeny {
		t.Fatalf("effective rule = %q, matched = %v", effect, matched)
	}
	exact, found := ExactHostResourceRule(rules, "docker")
	if !found || exact != ApprovalEffectAsk {
		t.Fatalf("exact rule = %q, found = %v", exact, found)
	}
}
