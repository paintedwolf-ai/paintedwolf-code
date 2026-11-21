package contract

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

// Summarize with a task but no material is refused by the served executor
// with a typed operand refusal before any work starts.
func TestNativeSummarizeNoInputStructuredRejectE2E(t *testing.T) {
	program := toolfixture.NewProgramFixture(t)
	raw, err := program.InvokeTool(t, "summarize", map[string]any{"task": "explain"}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	if err != nil {
		reject, ok := guidance.RefusalFromError(err)
		if !ok || reject.Code() != "COMMAND_OPERAND_INVALID" {
			t.Fatalf("expected typed reject COMMAND_OPERAND_INVALID, got %v", err)
		}
		return
	}
	var outcome command.ProgramOutcome
	testutil.FailErr(t, "decode program outcome", json.Unmarshal([]byte(raw), &outcome))
	if outcome.Refusal == nil || outcome.Refusal.Code != "COMMAND_OPERAND_INVALID" || outcome.Status == 0 {
		t.Fatalf("expected typed program refusal COMMAND_OPERAND_INVALID, got %+v", outcome)
	}
}

func assertStructuredRejectCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected structured reject %s, got nil", want)
	}
	reject, ok := guidance.RefusalFromError(err)
	if !ok {
		t.Fatalf("expected typed guidance.Refusal (%s), got %T: %v", want, err, err)
	}
	if reject.Code() != want {
		t.Fatalf("reject.Code() = %q want %q", reject.Code(), want)
	}
}
