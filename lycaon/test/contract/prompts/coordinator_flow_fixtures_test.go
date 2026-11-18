package contract

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/testutil/surfacetest"
	"github.com/lycaon/lycaon/pkg/api"
)

type flowParityCase struct {
	Name    string
	RunCtx  api.CoordinatorRunContext
	Sess    *api.Session
	History []api.Message
	State   surface.ImplementSessionState
}

func flowParityCases() []flowParityCase {
	var cases []flowParityCase
	cases = append(cases, flowParityCase{
		Name:    "compose_draft",
		RunCtx:  api.CoordinatorRunContext{HasComposeDraft: true},
		Sess:    &api.Session{Posture: api.SessionPostureBuild},
		History: parityTurnHistory("draft workflow"),
	})
	cases = append(cases, flowParityCase{
		Name:    "overlay_pending",
		State:   surface.ImplementSessionState{PendingOverlayIDs: []string{"overlay-1"}},
		Sess:    &api.Session{Posture: api.SessionPostureBuild},
		History: parityTurnHistory(surface.HostLoopWakeSentinel),
	})
	cases = append(cases, flowParityCase{
		Name:    "worker_delivered",
		State:   surface.ImplementSessionState{WorkersDelivered: 1},
		Sess:    &api.Session{Posture: api.SessionPostureBuild},
		History: parityTurnHistory(surface.HostLoopWakeSentinel),
	})
	for _, manifest := range []string{"plan_research", "plan_execute"} {
		cases = append(cases, flowParityCase{
			Name:   "manifest_" + manifest,
			RunCtx: api.CoordinatorRunContext{PhaseCoordinatorSurface: manifest, PhaseSurfaceTemplate: "agents/coordinator-surface-plan.md"},
			Sess:   &api.Session{Posture: api.SessionPostureSpec},
		})
	}
	for _, child := range []bool{false, true} {
		for _, declared := range []string{"", surface.ExecutionModeFamilyOrchestrate, surface.ExecutionModeFamilyInvestigate, surface.ExecutionModeFamilyWrapup} {
			for _, eligible := range []bool{false, true} {
				for _, workers := range []int{0, 1} {
					for _, prompt := range []string{"fix auth in src/", surface.HostLoopWakeSentinel, "Worker task finished: job-1"} {
						for _, wrapupLoaded := range []bool{false, true} {
							for _, openRepair := range []bool{false, true} {
								for _, batchReady := range []bool{false, true} {
									runCtx := api.CoordinatorRunContext{}
									if child {
										runCtx.RunStatus = string(api.WorkflowRunStatusPausedOnChild)
									}
									if declared != "" {
										runCtx.WorkflowDefaultExecutionMode = declared
									}
									eligibleCopy := eligible
									runCtx.WorkflowInvestigateEligible = &eligibleCopy
									sess := &api.Session{Posture: api.SessionPostureBuild}
									state := surface.ImplementSessionState{WorkersInFlight: workers}
									if wrapupLoaded {
										state = surfacetest.WithWrapupGates(state, batchReady, openRepair)
									}
									history := parityTurnHistory(prompt)
									name := fmt.Sprintf("core child=%t decl=%s elig=%t workers=%d wrap=%t repair=%t batch=%t prompt=%s",
										child, declared, eligible, workers,
										wrapupLoaded, openRepair, batchReady, parityPromptTag(prompt))
									cases = append(cases, flowParityCase{
										Name:    name,
										RunCtx:  runCtx,
										Sess:    sess,
										History: history,
										State:   state,
									})
								}
							}
						}
					}
				}
			}
		}
	}
	return cases
}

func parityTurnHistory(prompt string) []api.Message {
	switch prompt {
	case surface.HostLoopWakeSentinel:
		return []api.Message{{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Visibility: api.MessageVisibilityInternal, Kind: api.MessageKindHostLoopWake, Content: prompt}}
	case "Worker task finished: job-1":
		return []api.Message{{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Visibility: api.MessageVisibilityInternal, Kind: api.MessageKindHostKick, HostSignalID: anchor.WorkerTaskFinished.String(), Content: prompt}}
	default:
		return []api.Message{{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Visibility: api.MessageVisibilityTranscript, Content: prompt}}
	}
}

func parityPromptTag(prompt string) string {
	switch prompt {
	case surface.HostLoopWakeSentinel:
		return "host_wake"
	case "Worker task finished: job-1":
		return "worker_finished"
	default:
		return "visible_user"
	}
}
