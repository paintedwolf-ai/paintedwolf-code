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
		{"AC-COMPAT-09 bare report JSON has no answer", spawn.SurfaceImplementSynthesis, `{"synthesis":"Done.","cited_evidence":[{"evidence":"read#1"}]}`, true},
		{"AC-COMPAT-09 bare retired tool call has no answer", toolcontract.SurfaceImplementInvestigate, `{"name":"surface_note","arguments":{"summary":"Done."}}`, true},
		{"a bare JSON array has no answer", toolcontract.SurfaceImplementInvestigate, `[{"finding":"a"}]`, true},
		{"fenced JSON stays a literal answer", toolcontract.SurfaceImplementInvestigate, "```json\n{\"a\":1}\n```", false},
		{"JSON beside prose is prose", toolcontract.SurfaceImplementInvestigate, `Here it is: {"a":1}`, false},
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
