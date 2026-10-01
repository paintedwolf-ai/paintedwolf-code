package guidance

import (
	"context"
	"github.com/lycaon/lycaon/internal/extpacks"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFormatCoordinatorNudgeBatchWrongPhase(t *testing.T) {
	SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	cfg, err := LoadHintConfig(extpacks.Bundled(hintregistry.DefaultDir))
	testutil.FailErr(t, "LoadHintConfig failed", err)
	f := NewStaticRejectFormatter(cfg)
	raw, err := FormatCoordinatorNudge(context.Background(), f, "COORDINATOR_BATCH_WRONG_PHASE", nil)
	testutil.FailErr(t, "FormatCoordinatorNudge failed", err)
	if strings.Contains(raw, "Rejected:") {
		t.Fatalf("advisory misclassified as rejection: %s", raw)
	}
	for _, want := range []string{
		"Tool feedback",
		"Fix:",
		"Code: COORDINATOR_BATCH_WRONG_PHASE",
		"pack_board",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("missing %q in %q", want, raw)
		}
	}
}
