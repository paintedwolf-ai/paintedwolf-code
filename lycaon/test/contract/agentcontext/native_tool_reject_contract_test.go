package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

func assertStructuredRejectCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected structured reject %q", code)
	}
	reject, ok := guidance.RefusalFromError(err)
	if !ok || reject.Code() != code {
		t.Fatalf("expected typed reject %q, got %v", code, err)
	}
}

// Summarize with a task but no material is refused by the served executor
// with a typed operand refusal before any work starts.
func TestNativeSummarizeNoInputStructuredRejectE2E(t *testing.T) {
	program := toolfixture.NewProgramFixture(t)
	_, err := program.InvokeTool(t, "summarize", map[string]any{"task": "explain"}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	assertStructuredRejectCode(t, err, "COMMAND_OPERAND_INVALID")
}
