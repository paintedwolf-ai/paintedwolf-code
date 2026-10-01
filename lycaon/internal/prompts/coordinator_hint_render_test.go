package prompts_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCoordinatorScoutWriteHintTemplate(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(engine))
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)
	entry := cfg.HintCodes["COORDINATOR_ORCHESTRATE_WRITE_DENIED"]
	msg, _, fix := guidance.RenderHintFields("COORDINATOR_ORCHESTRATE_WRITE_DENIED", entry, map[string]any{
		"tool": "write", "turn_surface": "implement_park", "can_task": true,
	})
	// Rejections identify the offered surface and its available recovery action.
	if !strings.Contains(msg, "write") || !strings.Contains(msg, "implement_park surface") {
		t.Fatalf("message = %q want tool + current surface", msg)
	}
	if !strings.Contains(fix, "task") {
		t.Fatalf("expected task recovery, got %q", fix)
	}
}
