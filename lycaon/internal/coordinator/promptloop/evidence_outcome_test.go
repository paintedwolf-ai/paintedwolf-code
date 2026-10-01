package promptloop

import (
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFailedEffectEvidenceRetainsOutcomeWithoutInventingFileProof(t *testing.T) {
	for _, tc := range []struct {
		tool                  string
		invoked, hostAnswered bool
		status                api.InvocationStatus
		want                  bool
	}{
		{"command", true, false, api.InvocationStatusError, true},
		{"git_commit", true, false, api.InvocationStatusError, true},
		{"git_merge", true, false, api.InvocationStatusError, true},
		{"write", true, false, api.InvocationStatusError, false},
		{"read", true, false, api.InvocationStatusError, false},
		{"command", false, false, api.InvocationStatusError, false},
		{"command", true, true, api.InvocationStatusError, false},
		{"command", true, false, api.InvocationStatusInterrupted, false},
	} {
		contract, ok := toolcontract.Lookup(tc.tool)
		if !ok {
			t.Fatalf("missing contract for %s", tc.tool)
		}
		run := toolInvocation{contract: contract, hostAnswered: tc.hostAnswered,
			facts:   guidance.ToolResultFacts{Outcome: api.ToolResultOutcomeError},
			receipt: &api.InvocationReceipt{Invoked: tc.invoked, Status: tc.status}}
		if got := toolEvidenceEligible(run, tc.tool, nil); got != tc.want {
			t.Fatalf("case %+v: eligible=%v", tc, got)
		}
	}
}
