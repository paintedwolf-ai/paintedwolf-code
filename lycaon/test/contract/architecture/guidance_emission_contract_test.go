package contract

import (
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestRecoverableHintCodesRenderFullRejectBlock(t *testing.T) {
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	f := guidance.NewStaticRejectFormatter(cfg)

	for code, entry := range cfg.HintCodes {
		// OAR hints are never evaluated and carry no scenarios.
		if guidance.CopyOnlyEmit(entry.Emit) || entry.Kind == "hint" {
			continue
		}
		if entry.Severity == "info" && !strings.HasPrefix(entry.Emit, "guard:") && !strings.HasPrefix(entry.Emit, "rule:") {
			continue
		}
		if strings.TrimSpace(entry.What) == "" && strings.TrimSpace(entry.Message) == "" {
			t.Fatalf("hint %q missing what or message", code)
		}
		// Scenarios supply the template inputs.
		if len(entry.Scenarios) == 0 {
			t.Fatalf("hint %q missing scenarios", code)
		}
		assertBlock := func(label string, vars map[string]any) {
			raw, err := f.Format(code, vars)
			if err != nil {
				t.Fatalf("hint %q (%s) Format: %v", code, label, err)
			}
			if !strings.Contains(raw, "Code: "+code) {
				t.Fatalf("hint %q (%s) reject block lacks its code:\n%s", code, label, raw)
			}
		}
		assertBlock("default", map[string]any{
			"tool":    renderToolFor(entry, "read"),
			"path":    "src/main.go",
			"profile": "coordinator", "leg_id": "leg-1", "count": 3,
		})
		for _, sc := range entry.Scenarios {
			assertBlock(sc.ID, guidance.ScenarioVars(entry, sc))
		}
	}
}

func TestPreventiveRejectEmittersBackedByLiveHints(t *testing.T) {
	t.Parallel()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)

	live := make(map[string]bool)
	for _, entry := range cfg.HintCodes {
		if strings.TrimSpace(entry.Category) == "recoverable" {
			live[strings.TrimSpace(entry.Emit)] = true
		}
	}

	var orphaned []string
	for _, emit := range prompts.PreventiveRejectEmitters() {
		if !live[emit] {
			orphaned = append(orphaned, emit)
		}
	}
	sort.Strings(orphaned)
	if len(orphaned) > 0 {
		t.Fatalf(`preventiveRejectEmitters names guards with no live recoverable hint: %v

The prompt tool-surface table filters on emit; a rename or removal here silently drops codes from the table.
Fix: update prompts.preventiveRejectEmitters to match the guard's current emit, or remove the stale entry.`, orphaned)
	}
}
