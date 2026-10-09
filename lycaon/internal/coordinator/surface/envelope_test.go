package surface_test

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

func hostLoopHistory(history []api.Message) []api.Message {
	return append(history, api.Message{
		Role:       api.MessageRoleUser,
		Origin:     api.MessageOriginHost,
		Visibility: api.MessageVisibilityInternal,
		Kind:       api.MessageKindHostLoopWake,
		Content:    surface.HostLoopWakeSentinel,
	})
}

func workerFinishedHistory(history []api.Message) []api.Message {
	return append(history, api.Message{
		Role:         api.MessageRoleUser,
		Origin:       api.MessageOriginHost,
		Visibility:   api.MessageVisibilityInternal,
		Kind:         api.MessageKindHostKick,
		HostSignalID: anchor.WorkerTaskFinished.String(),
	})
}

func completeImplementerHistory() []api.Message {
	return []api.Message{{
		Role:    api.MessageRoleAssistant,
		Content: `<task job_id="j1" agent_type="implementer" state="complete"><summary>done</summary></task>`,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID: "j1",
			Status:   "complete",
		},
	}}
}

func TestHostCycleTurnSentinels(t *testing.T) {
	loopWake := hostLoopHistory(nil)
	workerFinished := workerFinishedHistory(nil)
	if !surface.HostLoopWakeTurn(loopWake) {
		t.Fatal("HostLoopWakeTurn must match tagged loop wake")
	}
	if !surface.WorkerTaskFinishedTurn(workerFinished) {
		t.Fatal("WorkerTaskFinishedTurn must match worker kick id")
	}
	if !surface.HostCycleTurn(workerFinished) {
		t.Fatal("HostCycleTurn must include worker-task-finished kicks")
	}
	if surface.HostCycleTurn([]api.Message{{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "fix auth"}}) {
		t.Fatal("visible user prompt must not be a host cycle turn")
	}
}

func TestHostCycleTurnSurvivesToolIterationsWithinTheSameTurn(t *testing.T) {
	history := []api.Message{{
		Role: api.MessageRoleUser, Origin: api.MessageOriginUser,
		Visibility: api.MessageVisibilityTranscript, Content: "build everything",
	}}
	history = hostLoopHistory(history)
	history = append(history,
		api.Message{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "call-1", Name: "promote_overlay"}}},
		api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{ToolCallID: "call-1", Tool: "promote_overlay"}},
	)
	if !surface.HostCycleTurn(history) {
		t.Fatal("tool iterations must not erase the host-cycle boundary")
	}
	if surface.ComputeSurfaceFacts(api.CoordinatorRunContext{}, nil, history, surface.ImplementSessionState{}).VisibleUserTurn {
		t.Fatal("tool iterations must not replay the visible-user routing override")
	}
	history = append(history, api.Message{
		Role: api.MessageRoleUser, Origin: api.MessageOriginUser,
		Visibility: api.MessageVisibilityTranscript, Content: "what remains?",
	})
	if surface.HostCycleTurn(history) {
		t.Fatal("a new visible user turn must end the prior host cycle")
	}
	if !surface.ComputeSurfaceFacts(api.CoordinatorRunContext{}, nil, history, surface.ImplementSessionState{}).VisibleUserTurn {
		t.Fatal("a new visible user message must restore visible-user routing")
	}
}

func TestResolveTurnProfileHostCycleAfterPromotion(t *testing.T) {
	history := hostLoopHistory([]api.Message{{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "build everything"}})
	history = append(history,
		api.Message{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "promote", Name: "promote_overlay"}}},
		api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{ToolCallID: "promote", Tool: "promote_overlay"}},
	)
	for _, tc := range []struct {
		name  string
		state surface.ImplementSessionState
		want  string
	}{
		{"siblings running", surface.ImplementSessionState{WorkersInFlight: 1}, surface.SurfaceImplementPark},
		{"work remains", surface.ImplementSessionState{ProgressOpenCount: 3, OpenRepairSinceUserIntent: true}, surface.SurfaceImplementDispatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := surface.ResolveTurnProfile(
				api.CoordinatorRunContext{WorkflowDefaultExecutionMode: "orchestrate"},
				&api.Session{Posture: api.SessionPostureBuild}, history, tc.state,
			)
			if profile.SurfaceID != tc.want {
				t.Fatalf("surface = %q, want %q", profile.SurfaceID, tc.want)
			}
		})
	}
}

func TestTerminalWorkerCompletionSinceRespectsVisibleUserBoundary(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "dispatch fixes"},
		{Role: api.MessageRoleAssistant, Content: `<task job_id="j1" agent_type="implementer" state="complete"><summary>done</summary></task>`,
			WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "j1", Status: "complete"},
		},
	}
	if !surface.TerminalWorkerCompletionSince(history, 1) {
		t.Fatal("expected terminal worker after visible user boundary")
	}
	inline := []api.Message{
		{Role: api.MessageRoleUser, Content: "fix physics"},
		{Role: api.MessageRoleAssistant, Content: "Reading pacifism.py."},
	}
	if surface.TerminalWorkerCompletionSince(inline, 1) {
		t.Fatal("inline investigate turn must not count as terminal worker completion")
	}
}

func TestResolveTurnProfileImplementerSynthesisWhenIdle(t *testing.T) {
	profile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{},
		&api.Session{Posture: api.SessionPostureBuild},
		hostLoopHistory(completeImplementerHistory()),
		surface.WithWrapupGates(surface.ImplementSessionState{}, true, false),
	)
	if profile.SurfaceID != "implement_synthesis" {
		t.Fatalf("surface = %q want implement_synthesis", profile.SurfaceID)
	}
	if profile.ModeRefs[0] != "implement-synthesis" {
		t.Fatalf("modeRefs = %v", profile.ModeRefs)
	}
}

func TestResolveTurnProfileImplementerParkWhenSiblingsInFlight(t *testing.T) {
	profile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{},
		&api.Session{Posture: api.SessionPostureBuild},
		hostLoopHistory(completeImplementerHistory()),
		surface.ImplementSessionState{WorkersInFlight: 2},
	)
	if profile.SurfaceID != surface.SurfaceImplementPark {
		t.Fatalf("surface = %q want %q", profile.SurfaceID, surface.SurfaceImplementPark)
	}
	if profile.ModeRefs[0] != "implement-park" {
		t.Fatalf("modeRefs = %v want implement-park", profile.ModeRefs)
	}
}

func TestResolveTurnProfileWorkerFinishedWithSiblingsUsesDispatch(t *testing.T) {
	profile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{},
		&api.Session{Posture: api.SessionPostureBuild},
		workerFinishedHistory(completeImplementerHistory()),
		surface.ImplementSessionState{WorkersInFlight: 2},
	)
	if profile.SurfaceID != surface.SurfaceImplementDispatch {
		t.Fatalf("surface = %q want %q", profile.SurfaceID, surface.SurfaceImplementDispatch)
	}
}

func pendingMergeHistory() []api.Message {
	return []api.Message{{
		Role: api.MessageRoleAssistant,
		Content: `<task job_id="j1" agent_type="implementer" state="complete" merge_status="pending">
  <summary>wrote file</summary>
</task>`,
		WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "j1", Status: api.WorkerSummaryStatusOpen},
	}}
}

func TestPendingOverlaySummaryIDs(t *testing.T) {
	ids := surface.PendingOverlaySummaryIDs(pendingMergeHistory())
	if len(ids) != 1 || ids[0] != "j1" {
		t.Fatalf("ids = %v want [j1]", ids)
	}
}

func TestPartialWorkerSummaryJobIDs(t *testing.T) {
	history := []api.Message{{
		Role:          api.MessageRoleAssistant,
		Content:       `<task job_id="j-partial" agent_type="implementer" state="partial"><summary>trimmed</summary></task>`,
		WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "j-partial", Status: "partial"},
	}}
	ids := surface.PartialWorkerSummaryJobIDs(history)
	if len(ids) != 1 || ids[0] != "j-partial" {
		t.Fatalf("ids = %v want [j-partial]", ids)
	}
}

func TestResolveTurnProfilePartialWithPendingOverlayUsesOverlayPromote(t *testing.T) {
	history := []api.Message{
		{
			Role:          api.MessageRoleAssistant,
			Content:       `<task job_id="j-partial" agent_type="implementer" state="partial" merge_status="pending"><summary>grounding partial</summary></task>`,
			WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "j-partial", Status: "partial"},
		},
	}
	profile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{},
		&api.Session{Posture: api.SessionPostureBuild},
		hostLoopHistory(history),
		surface.ImplementSessionState{PendingOverlayIDs: []string{"j-partial"}},
	)
	if profile.SurfaceID != surface.SurfaceImplementOverlayPromote {
		t.Fatalf("surface = %q want %q", profile.SurfaceID, surface.SurfaceImplementOverlayPromote)
	}
	if profile.ModeRefs[0] != "implement-overlay-promote" {
		t.Fatalf("modeRefs = %v", profile.ModeRefs)
	}
}

func TestResolveTurnProfileLedgerEmptyIgnoresStaleEnvelope(t *testing.T) {
	history := pendingMergeHistory()
	profile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{},
		&api.Session{Posture: api.SessionPostureBuild},
		hostLoopHistory(history),
		surface.ImplementSessionState{PendingOverlayIDs: []string{}},
	)
	if profile.SurfaceID == surface.SurfaceImplementOverlayPromote {
		t.Fatalf("surface = %q want investigate or synthesis after ledger clears pending overlays", profile.SurfaceID)
	}
}

func TestResolveTurnProfileOverlayPromoteSurface(t *testing.T) {
	profile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{},
		&api.Session{Posture: api.SessionPostureBuild},
		hostLoopHistory(completeImplementerHistory()),
		surface.ImplementSessionState{PendingOverlayIDs: []string{"j1"}},
	)
	if profile.SurfaceID != surface.SurfaceImplementOverlayPromote {
		t.Fatalf("surface = %q want %q", profile.SurfaceID, surface.SurfaceImplementOverlayPromote)
	}
	if profile.ModeRefs[0] != "implement-overlay-promote" {
		t.Fatalf("modeRefs = %v", profile.ModeRefs)
	}
}

func TestResolveTurnProfileSecuritySurveyReportHostCycleStaysSynthesis(t *testing.T) {
	profile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{
			WorkflowID:              "security-survey",
			CurrentPhase:            "report",
			PhaseCoordinatorSurface: spawn.SurfaceImplementSynthesis,
			RunStatus:               string(api.WorkflowRunStatusRunning),
		},
		&api.Session{Posture: api.SessionPostureVet},
		hostLoopHistory(nil),
	)
	if profile.SurfaceID != spawn.SurfaceImplementSynthesis {
		t.Fatalf("surface = %q want implement_synthesis until topology_report_delivered", profile.SurfaceID)
	}
}

func TestResolveTurnProfilePartialEnvelopeUsesSynthesis(t *testing.T) {
	history := []api.Message{{
		Role:    api.MessageRoleAssistant,
		Content: `<task job_id="j1" agent_type="implementer" state="partial"><summary>trimmed</summary></task>`,
		WorkerSummary: &api.WorkerSummaryMeta{
			Status: "partial",
		},
	}}
	profile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{},
		&api.Session{Posture: api.SessionPostureBuild},
		hostLoopHistory(history),
	)
	if profile.SurfaceID != toolcontract.SurfaceImplementInvestigate {
		t.Fatalf("surface = %q want implement_investigate for partial without batch readiness", profile.SurfaceID)
	}
}

func TestResolveTurnProfilePathExplorerUsesInvestigateWithoutBatchReady(t *testing.T) {
	history := []api.Message{{
		Role:          api.MessageRoleAssistant,
		Content:       `<task job_id="j1" agent_type="` + orchestration.ProfilePathExplorer + `" state="complete"><summary>ok</summary></task>`,
		WorkerSummary: &api.WorkerSummaryMeta{Status: "complete"},
	}}
	profile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{},
		&api.Session{Posture: api.SessionPostureBuild},
		hostLoopHistory(history),
	)
	if profile.SurfaceID != toolcontract.SurfaceImplementInvestigate {
		t.Fatalf("surface = %q want investigate without batch readiness", profile.SurfaceID)
	}
}

func TestResolveTurnProfilePathExplorerUsesSynthesisWhenBatchReady(t *testing.T) {
	history := []api.Message{{
		Role: api.MessageRoleAssistant,
		Content: `<task job_id="j1" agent_type="` + orchestration.ProfilePathExplorer + `" state="complete">
  <summary>surveyed layout</summary>
  <report_json>{"leg_status":"complete","files_modified":["pkg/api/types.go"]}</report_json>
</task>`,
		WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "j1", Status: "complete"},
	}}
	profile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{},
		&api.Session{Posture: api.SessionPostureBuild},
		hostLoopHistory(history),
		surface.WithWrapupGates(surface.ImplementSessionState{}, true, false),
	)
	if profile.SurfaceID != "implement_synthesis" {
		t.Fatalf("surface = %q want implement_synthesis", profile.SurfaceID)
	}
}
