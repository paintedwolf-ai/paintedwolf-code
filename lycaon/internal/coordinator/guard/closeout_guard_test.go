package guard_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestObserveCloseoutEnvelope(t *testing.T) {
	t.Parallel()

	rejectFmt := coordinatorRejectFmt(t)
	sess := &api.Session{Posture: api.SessionPostureBuild}

	t.Run("plain prose rejected on typed closeout surface", func(t *testing.T) {
		t.Parallel()
		reject, blocked := guard.FormatHostNoToolTurnReject(
			sess, hostLoopHistory(
				nil),
			"Plain prose only.", nil,
			spawn.SurfaceImplementSynthesis, true, surface.ImplementSessionState{}, rejectFmt, guard.BatchTurnGuard{})

		if !blocked {
			t.Fatal("expected plain prose reject")
		}
		if !strings.Contains(reject, guard.CoordinatorCloseoutEnvelopeOnlyCode) {
			t.Fatalf("reject = %q", reject)
		}
	})

	t.Run("envelope-only json accepted", func(t *testing.T) {
		t.Parallel()
		content := `{"synthesis":"Report body.","cited_evidence":[{"path":"a.go","line":1,"excerpt":"x"}]}`
		reject, blocked := guard.FormatHostNoToolTurnReject(
			sess, hostLoopHistory(
				nil),
			content, nil,
			tools.SurfaceImplementInvestigate, true, surface.ImplementSessionState{}, rejectFmt, guard.BatchTurnGuard{})

		if blocked {
			t.Fatalf("expected envelope json to pass guard, reject=%q", reject)
		}
	})

	t.Run("hybrid prose rejected", func(t *testing.T) {
		t.Parallel()
		content := "Here is my report.\n```json\n{\"synthesis\":\"Report body.\",\"cited_evidence\":[]}\n```"
		reject, blocked := guard.FormatHostNoToolTurnReject(
			sess, hostLoopHistory(
				nil),
			content, nil,
			tools.SurfaceImplementInvestigate, true, surface.ImplementSessionState{}, rejectFmt, guard.BatchTurnGuard{})

		if !blocked {
			t.Fatal("expected hybrid prose reject")
		}
		if !strings.Contains(reject, guard.CoordinatorCloseoutEnvelopeOnlyCode) {
			t.Fatalf("reject = %q", reject)
		}
	})

	t.Run("routing surface not required", func(t *testing.T) {
		t.Parallel()
		if reject, blocked := guard.FormatHostNoToolTurnReject(
			sess, hostLoopHistory(
				nil),
			"Plain prose only.", nil,
			"implement_routing", true, surface.ImplementSessionState{}, rejectFmt, guard.BatchTurnGuard{}); blocked {
			t.Fatalf("routing surface should not require closeout json: %q", reject)
		}
	})
}
