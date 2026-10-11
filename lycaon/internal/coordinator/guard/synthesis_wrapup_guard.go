package guard

import (
	"strings"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/spawn"
)

const CoordinatorSynthesisWrapupOnlyCode = "COORDINATOR_SYNTHESIS_WRAPUP_ONLY"

// ObserveCoordinatorSynthesisWrapupTool publishes wrapup-surface tool facts.
func ObserveCoordinatorSynthesisWrapupTool(surfaceID, toolName string, toolOffered bool, gc *oar.GuardContext) {
	if gc == nil {
		return
	}
	gc.Session.Surface = strings.TrimSpace(surfaceID)
	toolName = strings.TrimSpace(strings.ToLower(toolName))
	gc.Invocation.Tool = toolName
	gc.DeriveToolClassFacts()
	if gc.Session.Surface != spawn.SurfaceImplementSynthesis || toolName == "" {
		return
	}
	forbidden := !toolOffered
	gc.Workflow.SynthesisWrapupToolForbidden = forbidden
	if forbidden {
		gc.PutRejectData(CoordinatorSynthesisWrapupOnlyCode, map[string]any{"tool": toolName})
	}
}
