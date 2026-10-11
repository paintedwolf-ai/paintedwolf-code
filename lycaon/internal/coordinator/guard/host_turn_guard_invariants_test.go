package guard_test

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/pkg/api"
)

// implementHostSurfaceContract classifies prose-terminal surfaces.
var implementHostSurfaceContract = []struct {
	surface       string
	finishesProse bool
	closeoutJSON  bool
}{
	{"implement_routing", true, false},
	{"implement_synthesis", true, true},
	{toolcontract.SurfaceImplementInvestigate, true, true},
	{surface.SurfaceImplementDispatch, false, false},
	{surface.SurfaceImplementOverlayPromote, false, false},
	{surface.SurfaceImplementPark, false, false},
}

func groundedLegHistory(agentType string) []api.Message {
	envelope := `<task job_id="j1" child_session_id="child-1" agent_type="` + agentType + `" state="complete">
<task_result>retention.go surveyed</task_result>
</task>`
	return []api.Message{
		{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Kind: api.MessageKindHostLoopWake, Visibility: api.MessageVisibilityInternal, Content: surface.HostLoopWakeSentinel},
		{Role: api.MessageRoleTool, Content: envelope},
	}
}

func TestHostTurnGuard_allowMatrix(t *testing.T) {
	t.Parallel()
	for _, s := range implementHostSurfaceContract {
		if got := guard.HostTurnMayFinishWithProse(s.surface, true, false); got != s.finishesProse {
			t.Fatalf("%s: HostTurnMayFinishWithProse(idle, nothing pending) = %v, want %v", s.surface, got, s.finishesProse)
		}
		if guard.HostTurnMayFinishWithProse(s.surface, false, false) {
			t.Fatalf("%s: must block prose finish while workers are in flight", s.surface)
		}
		if guard.HostTurnMayFinishWithProse(s.surface, true, true) {
			t.Fatalf("%s: must block prose finish while an overlay promote is pending", s.surface)
		}
	}
}

func TestHostTurnGuard_fullChainMatrix(t *testing.T) {
	rejectFmt := coordinatorRejectFmt(t)
	history := groundedLegHistory(orchestration.ProfileImplementer)
	sess := &api.Session{WorkspacePath: t.TempDir(), Posture: api.SessionPostureBuild}
	grounded := coordinatorReportJSON(t, "The N+1 deletion loop batches deletes in retention.go.", []guidance.CoordinatorCitedEvidence{{
		Path: "internal/retention.go", Line: 1, Excerpt: "package retention",
	}})
	ungrounded := coordinatorReportJSON(t, "The hot loop is in phantom.go.", []guidance.CoordinatorCitedEvidence{{
		Path: "phantom.go", Line: 1, Excerpt: "package phantom",
	}})
	pendingOverlay := surface.ImplementSessionState{PendingOverlayIDs: []string{"job-1"}}

	chain := func(prose, surfaceID string, workersIdle bool, state surface.ImplementSessionState, turnTools []string) (string, bool) {
		return guard.FormatHostNoToolTurnReject(
			sess, hostLoopHistory(
				history),
			prose,
			turnTools, surfaceID, workersIdle, state, rejectFmt, guard.BatchTurnGuard{})

	}

	for _, s := range implementHostSurfaceContract {
		t.Run(s.surface, func(t *testing.T) {
			reject, block := chain(grounded, s.surface, true, surface.ImplementSessionState{}, nil)
			if s.finishesProse && !s.closeoutJSON {
				if block {
					t.Fatalf("idle grounded prose must finish on %s, got reject=%q", s.surface, reject)
				}
			} else if s.closeoutJSON {
				if block {
					t.Fatalf("idle envelope json must pass guard on %s, got reject=%q", s.surface, reject)
				}
			} else if !block || !strings.Contains(reject, guard.CoordinatorHostTurnRequiresWaitCode) {
				t.Fatalf("idle prose must require a tool terminal on %s, got reject=%q block=%v", s.surface, reject, block)
			}

			reject, block = chain(ungrounded, s.surface, true, surface.ImplementSessionState{}, nil)
			switch {
			case s.finishesProse && !s.closeoutJSON && block:
				t.Fatalf("ungrounded prose must not block on %s (non-closeout prose surface), got reject=%q", s.surface, reject)
			case s.closeoutJSON && block:
				t.Fatalf("ungrounded envelope json on %s must pass guard, got reject=%q block=%v", s.surface, reject, block)
			case !s.finishesProse && (!block || !strings.Contains(reject, guard.CoordinatorHostTurnRequiresWaitCode)):
				t.Fatalf("ungrounded prose must require a tool terminal on %s, got reject=%q block=%v", s.surface, reject, block)
			}

			if _, block = chain(grounded, s.surface, false, surface.ImplementSessionState{}, nil); !block {
				t.Fatalf("%s must block prose while workers are in flight", s.surface)
			}

			if _, block = chain(grounded, s.surface, true, pendingOverlay, nil); !block {
				t.Fatalf("%s must block prose while an overlay promote is pending", s.surface)
			}
		})
	}
}

func TestHostTurnGuard_readScoutSynthesisFinish(t *testing.T) {
	rejectFmt := coordinatorRejectFmt(t)
	sess := &api.Session{WorkspacePath: t.TempDir(), Posture: api.SessionPostureBuild}
	grounded := coordinatorReportJSON(t, "The retention cleanup batches deletes in retention.go.", []guidance.CoordinatorCitedEvidence{{
		Path: "internal/retention.go", Line: 1, Excerpt: "package retention",
	}})
	ungrounded := coordinatorReportJSON(t, "The hot path is in phantom.go.", []guidance.CoordinatorCitedEvidence{{
		Path: "phantom.go", Line: 1, Excerpt: "package phantom",
	}})

	chain := func(history []api.Message, surfaceID, prose string) (string, bool) {
		return guard.FormatHostNoToolTurnReject(
			sess, hostLoopHistory(
				history),
			prose,
			nil, surfaceID, true, surface.ImplementSessionState{}, rejectFmt, guard.BatchTurnGuard{})

	}

	history := groundedLegHistory(orchestration.ProfileRepoResearcher)
	if reject, block := chain(history, "implement_synthesis", grounded); block {
		t.Fatalf("read-scout synthesis turn must accept envelope json, got reject=%q block=%v", reject, block)
	}
	if reject, block := chain(history, "implement_synthesis", ungrounded); block {
		t.Fatalf("ungrounded read-scout synthesis must pass structural guard, got reject=%q block=%v", reject, block)
	}
	writerHistory := groundedLegHistory(orchestration.ProfileImplementer)
	if reject, block := chain(writerHistory, surface.SurfaceImplementDispatch, grounded); !block || !strings.Contains(reject, guard.CoordinatorHostTurnRequiresWaitCode) {
		t.Fatalf("writer leg on dispatch must require a tool terminal, got reject=%q block=%v", reject, block)
	}
}

func TestHostTurnGuard_hostCycleOpenPlanBlocksProse(t *testing.T) {
	rejectFmt := coordinatorRejectFmt(t)
	history := groundedLegHistory(orchestration.ProfileImplementer)
	sess := &api.Session{WorkspacePath: t.TempDir(), Posture: api.SessionPostureBuild}
	prose := coordinatorReportJSON(t, "Engine leg is done; AI and UI are next.", nil)
	state := surface.ImplementSessionState{ProgressOpenCount: 1}

	reject, block := guard.FormatHostNoToolTurnReject(
		sess, hostLoopHistory(
			history),
		prose,
		nil, toolcontract.SurfaceImplementInvestigate, true, state, rejectFmt, guard.BatchTurnGuard{})

	if !block || !strings.Contains(reject, guard.CoordinatorHostTurnRequiresWaitCode) {
		t.Fatalf("host cycle with open plan must block investigate prose, got reject=%q block=%v", reject, block)
	}

	reject, block = guard.FormatHostNoToolTurnReject(
		sess, hostLoopHistory(
			history),
		prose,
		nil, "implement_routing", true, state, rejectFmt, guard.BatchTurnGuard{})

	if !block || !strings.Contains(reject, guard.CoordinatorHostTurnRequiresWaitCode) {
		t.Fatalf("host cycle with open plan must block routing prose, got reject=%q block=%v", reject, block)
	}
}
