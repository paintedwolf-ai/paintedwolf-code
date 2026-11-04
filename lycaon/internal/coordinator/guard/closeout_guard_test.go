package guard_test

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

// D1: a Markdown closeout has an answer; only a draft with nothing to read fires.
func TestObserveCloseoutHasAnswer(t *testing.T) {
	t.Parallel()

	rejectFmt := coordinatorRejectFmt(t)
	sess := &api.Session{Posture: api.SessionPostureBuild}
	for _, tc := range []struct {
		name, surfaceID, content string
		blocked                  bool
	}{
		{"prose on the synthesis surface", spawn.SurfaceImplementSynthesis, "Plain prose only.", false},
		{"cited prose on the investigate surface", toolcontract.SurfaceImplementInvestigate, "Done [read#1:2].", false},
		{"AC-COMPAT-09 D15 a closeout may open with a fence", toolcontract.SurfaceImplementInvestigate, "```\nexample\n```\n\nDone.", false},
		{"AC-COMPAT-09 D15 a closeout may open with a brace", toolcontract.SurfaceImplementInvestigate, "{x} is the set.", false},
		{"directives alone", spawn.SurfaceImplementSynthesis, "[verify: targeted - Ran it]", true},
		{"citations alone", toolcontract.SurfaceImplementInvestigate, "[read#1:2]", true},
		{"routing surface is not a closeout", "implement_routing", "[verify: targeted - Ran it]", false},
		{"routing prose", "implement_routing", "Plain prose only.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reject, blocked := guard.FormatHostNoToolTurnReject(sess, hostLoopHistory(nil), tc.content, nil,
				tc.surfaceID, true, surface.ImplementSessionState{}, rejectFmt, guard.BatchTurnGuard{})
			if blocked != tc.blocked {
				t.Fatalf("blocked=%v want %v, reject=%q", blocked, tc.blocked, reject)
			}
			if tc.blocked && !strings.Contains(reject, guard.CoordinatorCloseoutNoAnswerCode) {
				t.Fatalf("reject = %q", reject)
			}
		})
	}
}
