package guard_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestHostTurnMayFinishWithProse(t *testing.T) {
	t.Parallel()
	// Always-prose surfaces: idle finish allowed; busy or pending overlay blocks.
	for _, surfaceID := range []string{"implement_synthesis", "implement_routing", "implement_investigate"} {
		if !guard.HostTurnMayFinishWithProse(surfaceID, true, false) {
			t.Fatalf("expected %s + idle to allow prose finish", surfaceID)
		}
		if guard.HostTurnMayFinishWithProse(surfaceID, false, false) {
			t.Fatalf("expected in-flight workers to block prose finish on %s", surfaceID)
		}
		if guard.HostTurnMayFinishWithProse(surfaceID, true, true) {
			t.Fatalf("expected pending overlay promote to block prose finish on %s", surfaceID)
		}
	}
	// Dispatch surface: always tool terminal — a prose-only finish is premature.
	if guard.HostTurnMayFinishWithProse(surface.SurfaceImplementDispatch, true, false) {
		t.Fatal("expected dispatch surface to require a tool terminal")
	}
}

func TestObserveHostTurnRequiresWaitLedgerOverridesStaleEnvelope(t *testing.T) {
	t.Parallel()
	fmt := coordinatorRejectFmt(t)
	sess := &api.Session{Posture: api.SessionPostureBuild}
	history := []api.Message{{
		Role:    api.MessageRoleAssistant,
		Content: `<task job_id="j1" agent_type="implementer" state="complete" merge_status="pending"><summary>wrote</summary></task>`,
	}}
	prose := coordinatorReportJSON(t, "All overlays are merged; here is the summary.", nil)
	reject, block := guard.FormatHostNoToolTurnReject(
		sess, hostLoopHistory(

			history),

		prose,
		nil,
		"implement_synthesis",
		true,
		surface.ImplementSessionState{PendingOverlayIDs: []string{}},
		fmt,
		guard.BatchTurnGuard{})

	if block {
		t.Fatalf("reject=%q block=%v want synthesis prose when ledger reports zero pending", reject, block)
	}
}

func TestObserveHostTurnRequiresWaitBlocksDispatchProse(t *testing.T) {
	fmt := coordinatorRejectFmt(t)
	sess := &api.Session{Posture: api.SessionPostureBuild}
	reject, block := guard.FormatHostNoToolTurnReject(
		sess, hostLoopHistory(

			nil),

		"Dispatching the next leg now.",
		nil,
		surface.SurfaceImplementDispatch,
		true,
		surface.ImplementSessionState{},
		fmt,
		guard.BatchTurnGuard{})

	if !block || !strings.Contains(reject, guard.CoordinatorHostTurnRequiresWaitCode) {
		t.Fatalf("reject=%q block=%v", reject, block)
	}
}

func TestObserveHostTurnRequiresWaitAllowsSynthesisProseWhenIdle(t *testing.T) {
	fmt := coordinatorRejectFmt(t)
	sess := &api.Session{Posture: api.SessionPostureBuild}
	prose := coordinatorReportJSON(t, "Here is the competitive assessment for your repo.", nil)
	reject, block := guard.FormatHostNoToolTurnReject(
		sess, hostLoopHistory(

			nil),

		prose,
		nil,
		"implement_synthesis",
		true,
		surface.ImplementSessionState{},
		fmt,
		guard.BatchTurnGuard{})

	if block {
		t.Fatalf("reject=%q block=%v want allow synthesis prose when idle", reject, block)
	}
}

func TestObserveHostTurnAfterPromotionRequiresRemainingWork(t *testing.T) {
	fmt := coordinatorRejectFmt(t)
	sess := &api.Session{Posture: api.SessionPostureBuild}
	history := hostLoopHistory([]api.Message{{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "build everything"}})
	history = append(history,
		api.Message{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "promote", Name: "promote_overlay"}}},
		api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{ToolCallID: "promote", Tool: "promote_overlay"}},
	)
	reject, block := guard.FormatHostNoToolTurnReject(
		sess, history, "Both implementation legs have landed. Here is where things stand.", nil,
		"implement_investigate", true,
		surface.ImplementSessionState{ProgressOpenCount: 5, OpenRepairSinceUserIntent: true},
		fmt, guard.BatchTurnGuard{},
	)
	if !block || !strings.Contains(reject, guard.CoordinatorHostTurnRequiresWaitCode) {
		t.Fatalf("open work after promotion must block prose closeout: reject=%q block=%v", reject, block)
	}
}

func TestObserveHostTurnRequiresWaitBlocksSynthesisProseWhenWorkersInFlight(t *testing.T) {
	fmt := coordinatorRejectFmt(t)
	sess := &api.Session{Posture: api.SessionPostureBuild}
	reject, block := guard.FormatHostNoToolTurnReject(
		sess, hostLoopHistory(

			nil),

		"Partial synthesis while siblings run.",
		nil,
		"implement_synthesis",
		false,
		surface.ImplementSessionState{},
		fmt,
		guard.BatchTurnGuard{})

	if !block || !strings.Contains(reject, guard.CoordinatorHostTurnRequiresWaitCode) {
		t.Fatalf("reject=%q block=%v", reject, block)
	}
}
