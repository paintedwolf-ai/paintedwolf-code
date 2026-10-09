package wiring

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/pkg/api"
	dealfinder "github.com/lycaon/lycaon/test/wiring/fixtures/synthesis_wrapup_deal_finder"
)

// Wrapup routing and tool availability follow structured state.
func TestCoordinatorSynthesisWrapupReplay(t *testing.T) {
	fix := dealfinder.Load(t)
	if fix.SessionID == "" {
		t.Fatal("fixture missing session_id")
	}

	for _, tc := range fix.Surface {
		t.Run("surface/"+tc.Name, func(t *testing.T) {
			state := wrapupStateFromSnapshot(tc.State, tc.History, tc.Progress, tc.WantVerifyPassed, tc.WantVerifyFailed)
			if got := workeroutcomes.BatchReadyForSynthesis(state, tc.History, tc.Progress, true, tc.WantVerifyPassed); got != tc.WantBatchReady {
				t.Fatalf("BatchReadyForSynthesis = %v want %v", got, tc.WantBatchReady)
			}
			if got := workeroutcomes.OpenRepairSinceUserIntent(tc.History, tc.WantVerifyFailed); got != tc.WantOpenRepair {
				t.Fatalf("OpenRepairSinceUserIntent = %v want %v", got, tc.WantOpenRepair)
			}

			profile := surface.ResolveTurnProfile(
				api.CoordinatorRunContext{WorkflowID: "implement", CurrentPhase: "work"},
				&api.Session{Posture: api.SessionPostureBuild},
				routingTurnHistory(tc.History, tc.UserPrompt),
				state,
			)
			if profile.SurfaceID != tc.WantSurface {
				t.Fatalf("surface = %q want %q", profile.SurfaceID, tc.WantSurface)
			}
		})
	}

	rejectFmt := wrapupReplayRejectFormatter(t)
	for _, tc := range fix.GuardCases {
		t.Run("guard/"+tc.Name, func(t *testing.T) {
			gc := oar.NewGuardContext()
			guard.ObserveCoordinatorSynthesisWrapupTool(tc.Surface, tc.Tool, tc.ToolOffered, gc)
			if tc.WantCode == "" {
				if len(gc.RejectData) != 0 {
					t.Fatalf("offered tool was rejected: %v", gc.RejectData)
				}
				return
			}
			// The guard records facts and reject data for policy evaluation.
			data, observed := gc.RejectData[tc.WantCode]
			if !observed {
				t.Fatalf("guard did not stamp reject data for %q; forbidden=%v", tc.WantCode, gc.SynthesisWrapupToolForbidden)
			}
			if tc.WantCode == guard.CoordinatorSynthesisWrapupOnlyCode && !gc.SynthesisWrapupToolForbidden {
				t.Fatal("synthesis_wrapup_tool_forbidden fact is unset")
			}
			formatted, err := rejectFmt.Format(tc.WantCode, data)
			if err != nil {
				t.Fatalf("Format: %v", err)
			}
			if !strings.Contains(formatted, tc.WantCode) {
				t.Fatalf("formatted = %q want code %q", formatted, tc.WantCode)
			}
		})
	}
}

func wrapupStateFromSnapshot(snap dealfinder.SnapshotState, history []api.Message, progress string, verifyPassed, verifyFailed bool) surface.ImplementSessionState {
	state := surface.ImplementSessionState{
		WorkersInFlight:   snap.WorkersInFlight,
		PendingOverlayIDs: snap.PendingOverlayIDs,
		BatchPhase:        snap.BatchPhase,
		WrapupGatesLoaded: true,
	}
	state.BatchReadyForSynthesis = workeroutcomes.BatchReadyForSynthesis(state, history, progress, true, verifyPassed)
	state.OpenRepairSinceUserIntent = workeroutcomes.OpenRepairSinceUserIntent(history, verifyFailed)
	return state
}

func wrapupReplayRejectFormatter(t *testing.T) *guidance.StaticRejectFormatter {
	t.Helper()
	cfg, err := guidance.LoadHintConfigStock()
	if err != nil {
		t.Fatalf("LoadHintConfig: %v", err)
	}
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	return guidance.NewStaticRejectFormatter(cfg)
}
