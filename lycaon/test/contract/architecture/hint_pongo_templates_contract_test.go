package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestRejectHintCodesRenderUnifiedBlock(t *testing.T) {
	cfg, err := guidance.LoadValidatedHintConfig(extpacks.Bundled(hintregistry.DefaultDir))
	contractcheck.FailErr(t, "LoadValidatedHintConfig", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	f := guidance.NewStaticRejectFormatter(cfg)

	for code, entry := range cfg.HintCodes {
		emit := strings.TrimSpace(entry.Emit)
		if guidance.CopyOnlyEmit(emit) {
			continue
		}
		if entry.Severity == "info" && !strings.HasPrefix(emit, "rule:") && !strings.HasPrefix(emit, "guard:") {
			continue
		}
		raw, err := f.Format(code, map[string]any{
			"tool":    renderToolFor(entry, "read"),
			"path":    "src/main.go",
			"profile": "coordinator", "detail": "bad args",
		})
		if err != nil {
			t.Fatalf("hint %q Format: %v", code, err)
		}
		for _, want := range []string{"Code: " + code, "Fix:"} {
			if !strings.Contains(raw, want) {
				t.Fatalf("hint %q reject block missing %q:\n%s", code, want, raw)
			}
		}
	}
}
