package surface

import "strings"

// Surface fact names for coordinator-flow.yaml condition cells.
const (
	FactHasComposeDraft                       = "has_compose_draft"
	FactManifestBoundSurface                  = "manifest_bound_surface"
	FactOverlayPromotePending                 = "overlay_promote_pending"
	FactChildSubroutineBlocksInvestigate      = "child_subroutine_blocks_investigate"
	FactWorkflowDeclaredMode                  = "workflow_declared_mode"
	FactInvestigateDefaultEligible            = "investigate_default_eligible"
	FactInvestigateHardBlock                  = "investigate_hard_block"
	FactWorkersInFlight                       = "workers_in_flight"
	FactHostCycleTurn                         = "host_cycle_turn"
	FactWorkerTaskFinishedTurn                = "worker_task_finished_turn"
	FactHostLoopWakeTurn                      = "host_loop_wake_turn"
	FactOpenRepairSinceUserIntent             = "open_repair_since_user_intent"
	FactBatchReadyForSynthesis                = "batch_ready_for_synthesis"
	FactWrapupGatesLoaded                     = "wrapup_gates_loaded"
	FactVerifyUnverified                      = "verify_unverified"
	FactVisibleUserTurn                       = "visible_user_turn"
	FactPosture                               = "posture"
	FactProgressHasOpenSteps                  = "progress_has_open_steps"
	FactProgressMissing                       = "progress_missing"
	FactProgressGatedToolAttemptedSinceIntent = "progress_gated_tool_attempted_since_intent"
	FactPhaseHostHeld                         = "phase_host_held"
)

type surfaceFactKind int

const (
	surfaceFactKindBool surfaceFactKind = iota
	surfaceFactKindEnum
	surfaceFactKindCount
)

var factKinds = map[string]surfaceFactKind{
	FactHasComposeDraft:                       surfaceFactKindBool,
	FactManifestBoundSurface:                  surfaceFactKindEnum,
	FactOverlayPromotePending:                 surfaceFactKindCount,
	FactChildSubroutineBlocksInvestigate:      surfaceFactKindBool,
	FactWorkflowDeclaredMode:                  surfaceFactKindEnum,
	FactInvestigateDefaultEligible:            surfaceFactKindBool,
	FactInvestigateHardBlock:                  surfaceFactKindBool,
	FactWorkersInFlight:                       surfaceFactKindCount,
	FactHostCycleTurn:                         surfaceFactKindBool,
	FactWorkerTaskFinishedTurn:                surfaceFactKindBool,
	FactHostLoopWakeTurn:                      surfaceFactKindBool,
	FactOpenRepairSinceUserIntent:             surfaceFactKindBool,
	FactBatchReadyForSynthesis:                surfaceFactKindBool,
	FactWrapupGatesLoaded:                     surfaceFactKindBool,
	FactVerifyUnverified:                      surfaceFactKindBool,
	FactVisibleUserTurn:                       surfaceFactKindBool,
	FactPosture:                               surfaceFactKindEnum,
	FactProgressHasOpenSteps:                  surfaceFactKindBool,
	FactProgressMissing:                       surfaceFactKindBool,
	FactProgressGatedToolAttemptedSinceIntent: surfaceFactKindBool,
	FactPhaseHostHeld:                         surfaceFactKindBool,
}

// registeredSurfaceFactNames is the closed vocabulary IsKnownFact and closure tests use.
var registeredSurfaceFactNames = []string{
	FactHasComposeDraft,
	FactManifestBoundSurface,
	FactOverlayPromotePending,
	FactChildSubroutineBlocksInvestigate,
	FactWorkflowDeclaredMode,
	FactInvestigateDefaultEligible,
	FactInvestigateHardBlock,
	FactWorkersInFlight,
	FactHostCycleTurn,
	FactWorkerTaskFinishedTurn,
	FactHostLoopWakeTurn,
	FactOpenRepairSinceUserIntent,
	FactBatchReadyForSynthesis,
	FactWrapupGatesLoaded,
	FactVerifyUnverified,
	FactVisibleUserTurn,
	FactPosture,
	FactProgressHasOpenSteps,
	FactProgressMissing,
	FactProgressGatedToolAttemptedSinceIntent,
	FactPhaseHostHeld,
}

// IsKnownFact reports whether name may appear in coordinator-flow.yaml when/dispatch_on cells.
func IsKnownFact(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	_, ok := factKinds[name]
	return ok
}

// RegisteredSurfaceFactNames returns the closed fact vocabulary (stable order for tests).
func RegisteredSurfaceFactNames() []string {
	out := make([]string, len(registeredSurfaceFactNames))
	copy(out, registeredSurfaceFactNames)
	return out
}
