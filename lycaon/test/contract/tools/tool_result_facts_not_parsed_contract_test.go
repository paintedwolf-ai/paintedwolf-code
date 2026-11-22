package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

// refusalVocabularyProse packs the phrases a prose matcher would reach for into
// a body whose outcome is a successful read.
const refusalVocabularyProse = `# Denying a tool call

Rejected: the host refuses a call whose Code: names a retired grant.
Denied by policy, blocked at the boundary, and refused before dispatch all
describe the same outcome. An approval that is not granted leaves the call
withheld; the error is reported to the coordinator as a structured reject.
`

// The composer carries subsystem facts, so a body using the refusal
// vocabulary still settles completed with no code.
func TestRefusalVocabularyInAResultBodyIsACompletedResult(t *testing.T) {
	t.Parallel()
	tr := guidance.ComposeToolResult(refusalVocabularyProse, guidance.ToolResultFacts{}, nil)
	if tr == nil {
		t.Fatal("expected a tool result")
	}
	if tr.Outcome != api.ToolResultOutcomeCompleted {
		t.Fatalf("outcome = %q want completed — the body discusses refusals, it is not one", tr.Outcome)
	}
	if len(tr.Codes) != 0 {
		t.Fatalf("codes = %v want none — no producer raised one", tr.Codes)
	}
}
