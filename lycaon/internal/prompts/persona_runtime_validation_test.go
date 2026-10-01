package prompts

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRuntimePersonaValidationDoesNotEnforceAuthoringByteCap(t *testing.T) {
	ResetPersonaContractCache()
	engine := NewFileTemplateEngineLayers(PromptLayers{})
	body, err := renderPersona(context.Background(), engine, "repo-researcher", nil, false)
	testutil.FailErr(t, "render repo-researcher persona", err)

	budgets, err := LoadPromptBudgets()
	testutil.FailErr(t, "load prompt budgets", err)
	capBytes := budgets.WorkerPersonas["repo-researcher"]
	if capBytes <= 0 {
		t.Fatal("repo-researcher authoring byte cap is not configured")
	}
	if len(body) <= capBytes {
		body += strings.Repeat("x", capBytes-len(body)+1)
	}

	authoringViolations, err := ValidatePersonaRender("repo-researcher", body)
	testutil.FailErr(t, "validate authoring persona render", err)
	if !containsViolationPrefix(authoringViolations, "budget_exceeded:repo-researcher:") {
		t.Fatalf("authoring violations = %v, want budget_exceeded", authoringViolations)
	}

	runtimeViolations, err := validatePersonaContractRender("repo-researcher", body)
	testutil.FailErr(t, "validate runtime persona render", err)
	if containsViolationPrefix(runtimeViolations, "budget_exceeded:") {
		t.Fatalf("runtime violations must not enforce authoring byte cap: %v", runtimeViolations)
	}
}

func containsViolationPrefix(violations []string, prefix string) bool {
	for _, violation := range violations {
		if strings.HasPrefix(violation, prefix) {
			return true
		}
	}
	return false
}
