package surface

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// SurfaceFacts is the typed fact bundle EvaluateFlow branches on.
type SurfaceFacts struct {
	HasComposeDraft                       bool
	ManifestBoundSurface                  string
	OverlayPromotePending                 int
	ChildSubroutineBlocksInvestigate      bool
	WorkflowDeclaredMode                  string
	InvestigateDefaultEligible            bool
	InvestigateHardBlock                  bool
	WorkersInFlight                       int
	HostCycleTurn                         bool
	WorkerTaskFinishedTurn                bool
	HostLoopWakeTurn                      bool
	OpenRepairSinceUserIntent             bool
	BatchReadyForSynthesis                bool
	WrapupGatesLoaded                     bool
	VerifyUnverified                      bool
	VisibleUserTurn                       bool
	Posture                               string
	ProgressHasOpenSteps                  bool
	ProgressMissing                       bool
	ProgressGatedToolAttemptedSinceIntent bool
	PhaseHostHeld                         bool
}

// ComputeSurfaceFacts collects every predicate coordinator-flow rules read via existing helpers.
func ComputeSurfaceFacts(
	runCtx api.CoordinatorRunContext,
	sess *api.Session,
	history []api.Message,
	state ImplementSessionState,
) SurfaceFacts {
	return SurfaceFacts{
		HasComposeDraft:                       runCtx.HasComposeDraft,
		ManifestBoundSurface:                  strings.TrimSpace(runCtx.PhaseCoordinatorSurface),
		OverlayPromotePending:                 len(state.PendingOverlayIDs),
		ChildSubroutineBlocksInvestigate:      childSubroutineBlocksInvestigate(runCtx),
		WorkflowDeclaredMode:                  normalizeWorkflowDeclaredMode(workflowDeclaredExecutionMode(runCtx)),
		InvestigateDefaultEligible:            workflowInvestigateEligible(runCtx),
		InvestigateHardBlock:                  investigateHardBlockActive(runCtx, history, state),
		WorkersInFlight:                       state.WorkersInFlight,
		HostCycleTurn:                         HostCycleTurn(history),
		WorkerTaskFinishedTurn:                WorkerTaskFinishedTurn(history),
		HostLoopWakeTurn:                      HostLoopWakeTurn(history),
		OpenRepairSinceUserIntent:             state.OpenRepairSinceUserIntent,
		BatchReadyForSynthesis:                state.BatchReadyForSynthesis,
		WrapupGatesLoaded:                     state.WrapupGatesLoaded,
		VerifyUnverified:                      state.VerifyUnverified,
		VisibleUserTurn:                       isVisibleUserTurn(history),
		Posture:                               postureString(sessionPosture(sess)),
		ProgressHasOpenSteps:                  state.ProgressOpenCount > 0,
		ProgressMissing:                       state.ProgressMissing,
		ProgressGatedToolAttemptedSinceIntent: state.ProgressGatedToolAttemptedSinceIntent,
		PhaseHostHeld:                         runCtx.PhaseHostHeld,
	}
}

// AsMap emits the fact map the flow evaluator consumes (vars["gates"] convention).
func (f SurfaceFacts) AsMap() map[string]any {
	return map[string]any{
		FactHasComposeDraft:                       f.HasComposeDraft,
		FactManifestBoundSurface:                  f.ManifestBoundSurface,
		FactOverlayPromotePending:                 f.OverlayPromotePending,
		FactChildSubroutineBlocksInvestigate:      f.ChildSubroutineBlocksInvestigate,
		FactWorkflowDeclaredMode:                  f.WorkflowDeclaredMode,
		FactInvestigateDefaultEligible:            f.InvestigateDefaultEligible,
		FactInvestigateHardBlock:                  f.InvestigateHardBlock,
		FactWorkersInFlight:                       f.WorkersInFlight,
		FactHostCycleTurn:                         f.HostCycleTurn,
		FactWorkerTaskFinishedTurn:                f.WorkerTaskFinishedTurn,
		FactHostLoopWakeTurn:                      f.HostLoopWakeTurn,
		FactOpenRepairSinceUserIntent:             f.OpenRepairSinceUserIntent,
		FactBatchReadyForSynthesis:                f.BatchReadyForSynthesis,
		FactWrapupGatesLoaded:                     f.WrapupGatesLoaded,
		FactVerifyUnverified:                      f.VerifyUnverified,
		FactVisibleUserTurn:                       f.VisibleUserTurn,
		FactPosture:                               f.Posture,
		FactProgressHasOpenSteps:                  f.ProgressHasOpenSteps,
		FactProgressMissing:                       f.ProgressMissing,
		FactProgressGatedToolAttemptedSinceIntent: f.ProgressGatedToolAttemptedSinceIntent,
		FactPhaseHostHeld:                         f.PhaseHostHeld,
	}
}

func normalizeWorkflowDeclaredMode(mode string) string {
	mode = strings.TrimSpace(strings.ToLower(mode))
	switch mode {
	case ExecutionModeFamilyInvestigate, ExecutionModeFamilyOrchestrate, ExecutionModeFamilyWrapup:
		return mode
	default:
		return ""
	}
}

func postureString(posture api.SessionPosture) string {
	switch posture {
	case api.SessionPostureSpec:
		return "spec"
	case api.SessionPostureBuild:
		return "build"
	case api.SessionPostureOrchestrate:
		return "orchestrate"
	case api.SessionPostureVet:
		return "vet"
	default:
		return "build"
	}
}
