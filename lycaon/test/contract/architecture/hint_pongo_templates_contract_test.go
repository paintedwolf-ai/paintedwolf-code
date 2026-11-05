package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/oarcopy"
	"github.com/lycaon/lycaon/internal/oarcore"
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
		lead := "Rejected:"
		if strings.TrimSpace(entry.Effect) == "warn" || strings.TrimSpace(entry.Effect) == "nudge" {
			lead = ">>>"
		}
		assertRender := func(label string, vars map[string]any) {
			raw, err := f.Format(code, vars)
			if err != nil {
				t.Fatalf("hint %q (%s) Format: %v", code, label, err)
			}
			for _, want := range []string{lead, "Code: " + code} {
				if !strings.Contains(raw, want) {
					t.Fatalf("hint %q (%s) reject block missing %q:\n%s", code, label, want, raw)
				}
			}
			fix := strings.TrimSpace(oarcore.RenderCopy(entry.Fix, oarcopy.FactsFromData(vars)))
			if strings.Contains(raw, "Fix:") != (fix != "") {
				t.Fatalf("hint %q (%s) Fix line mismatch for fix=%q:\n%s", code, label, fix, raw)
			}
		}
		assertRender("default", map[string]any{
			"tool":    renderToolFor(entry, "read"),
			"path":    "src/main.go",
			"profile": "coordinator", "detail": "bad args",
		})
		for _, sc := range entry.Scenarios {
			assertRender(sc.ID, guidance.ScenarioVars(entry, sc))
		}
	}
}
