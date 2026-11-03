package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCommandAdmissionGuidanceKeepsProfileSpanAndReachableRemedy(t *testing.T) {
	config, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load command admission policy", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	formatter := guidance.NewStaticRejectFormatter(config)
	for _, code := range []string{"COMMAND_EFFECT_NOT_ALLOWED", "COMMAND_OPERATION_NOT_ALLOWED"} {
		for _, form := range []string{"sed [stream-script]", ""} {
			output, err := formatter.Format(code, map[string]any{"tool": "command", "permission_profile": "worker_readonly", "source_line": 2, "source_column": 3, "expected_form": form})
			contractcheck.FailErr(t, "render command admission refusal", err)
			for _, want := range []string{"Code: " + code, "worker_readonly", "2:3", "Earlier", "Do not retry through external"} {
				if !strings.Contains(output, want) {
					t.Fatalf("%s lost %q: %s", code, want, output)
				}
			}
			if form != "" && !strings.Contains(output, form) {
				t.Fatalf("%s lost exact permitted modes", code)
			}
			if form == "" && !strings.Contains(output, "no available form") {
				t.Fatalf("%s invented unavailable repair", code)
			}
		}
	}
}
