package guard

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
)

func TestObserveCoordinatorSynthesisWrapupTool_edit(t *testing.T) {
	fmt := wrapupGuardFormatter(t)
	text, block := formatObservationReject(t, fmt, func(gc *oar.GuardContext) {
		ObserveCoordinatorSynthesisWrapupTool(spawn.SurfaceImplementSynthesis, "edit", false, gc)
	})
	if !block || !strings.Contains(text, CoordinatorSynthesisWrapupOnlyCode) {
		t.Fatalf("block=%v text=%q", block, text)
	}
}

func TestObserveCoordinatorSynthesisWrapupTool_readAllowed(t *testing.T) {
	fmt := wrapupGuardFormatter(t)
	_, block := formatObservationReject(t, fmt, func(gc *oar.GuardContext) {
		ObserveCoordinatorSynthesisWrapupTool(spawn.SurfaceImplementSynthesis, "read", true, gc)
	})
	if block {
		t.Fatal("read must be allowed on wrapup")
	}
}

func TestObserveCoordinatorSynthesisWrapupTool_otherSurface(t *testing.T) {
	fmt := wrapupGuardFormatter(t)
	_, block := formatObservationReject(t, fmt, func(gc *oar.GuardContext) {
		ObserveCoordinatorSynthesisWrapupTool("implement_dispatch", "edit", false, gc)
	})
	if block {
		t.Fatal("non-synthesis surface must not observe")
	}
}

func wrapupGuardFormatter(t *testing.T) *guidance.StaticRejectFormatter {
	t.Helper()
	cfg, err := guidance.LoadHintConfigStock()
	if err != nil {
		t.Fatalf("LoadHintConfig: %v", err)
	}
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	return guidance.NewStaticRejectFormatter(cfg)
}

func TestSynthesisGuardUsesRequestAvailabilityForNewTools(t *testing.T) {
	for _, offered := range []bool{false, true} {
		gc := oar.NewGuardContext()
		ObserveCoordinatorSynthesisWrapupTool(spawn.SurfaceImplementSynthesis, "new_catalog_tool", offered, gc)
		if gc.SynthesisWrapupToolForbidden == offered {
			t.Fatalf("new tool availability %v did not control the observation: %+v", offered, gc)
		}
	}
}
