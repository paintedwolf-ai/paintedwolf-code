package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestWaitVisibilityGuidanceOffersBothRecoveryPaths(t *testing.T) {
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load wait guidance", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	out, err := guidance.NewStaticRejectFormatter(cfg).Format("WAIT_HIDDEN_TEXT", map[string]any{"tool": "wait", "observed_handles_sample": "task#1"})
	contractcheck.FailErr(t, "render wait guidance", err)
	for _, want := range []string{"Code: WAIT_HIDDEN_TEXT", "has not started", "unpublished draft", "task#1", "Empty text does not clear this", "same wait conditions"} {
		if !strings.Contains(out, want) {
			t.Fatalf("wait recovery missing %q: %s", want, out)
		}
	}
}
