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
	reader, err := DeclaredReader(nil, "repo-researcher")
	testutil.FailErr(t, "DeclaredReader repo-researcher", err)
	personaEngine, _, ref, data, err := personaVars(context.Background(), engine, "repo-researcher", reader, nil)
	testutil.FailErr(t, "repo-researcher persona vars", err)
	body, err := personaEngine.Render(context.Background(), ref, data)
	testutil.FailErr(t, "render repo-researcher persona", err)

	budgets, err := LoadPromptBudgets()
	testutil.FailErr(t, "load prompt budgets", err)
	capBytes := budgets.Sizes.Cap("worker_personas", "repo-researcher")
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
