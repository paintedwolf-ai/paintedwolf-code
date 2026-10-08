package prompts

import (
	"strings"
	"testing"
)

func TestPromptSizesCapPrefersAnExceptionOverTheCategoryLimit(t *testing.T) {
	t.Parallel()
	sizes := PromptSizes{
		Limits:     map[string]SizeLimit{"worker_personas": {Warn: 16, Limit: 20}},
		Exceptions: map[string]map[string]SizeException{"worker_personas": {"special": {Cap: 40, Reason: "r"}}},
	}
	for id, want := range map[string]int{"plain": 20, "special": 40} {
		if got := sizes.Cap("worker_personas", id); got != want {
			t.Errorf("Cap(%s) = %d, want %d", id, got, want)
		}
	}
}

func TestDecodePromptBudgetsRequiresThePersonaLimit(t *testing.T) {
	t.Parallel()
	const valid = `version: 1
sizes:
    limits:
        worker_personas: {warn: 16, limit: 20}
agents_md_inject:
    max_body_bytes: 10
perception:
    max_tool_images: 2
    drop_batch: 1
`
	if _, err := decodePromptBudgets([]byte(valid)); err != nil {
		t.Fatalf("decode valid prompt budgets: %v", err)
	}
	missing := strings.Replace(valid, "        worker_personas: {warn: 16, limit: 20}\n", "", 1)
	if _, err := decodePromptBudgets([]byte(missing)); err == nil || !strings.Contains(err.Error(), "worker_personas") {
		t.Fatalf("decode without persona limit: err = %v, want the missing worker_personas limit", err)
	}
}
