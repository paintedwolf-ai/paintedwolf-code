package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/pkg/api"
)

// A prose citation leaks only when the ledger observed it: a path-shaped token
// nobody observed is not flagged, and an observed token is flagged whatever its
// shape.
func TestLeakDetectorUsesObservedSetNotShapeRegex(t *testing.T) {
	t.Parallel()
	const observed, line = "Makefile", 3
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": observed, "offset": line, "limit": 1}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: `{"path":"Makefile","content":"3|  build:","offset":3,"end_line":3,"limit":1}`,
		}},
	}
	ev := ledgertest.BuildFromMessages("", msgs)
	typed := guidance.BuildWorkerTypedChannel(evidence.CitationRoots{}, ev, nil, nil)

	if leaks := guidance.ProseLeaks("The bug is in internal/auth/handler.go:42", ev, typed); len(leaks) != 0 {
		t.Fatalf("unobserved path-shaped citation flagged: %+v", leaks)
	}
	leaks := guidance.ProseLeaks("The target is defined at Makefile:3", ev, typed)
	if len(leaks) != 1 || leaks[0].Token != "Makefile:3" || leaks[0].Kind != evidence.ObservedCitationKindPathLine || leaks[0].AlsoInTyped {
		t.Fatalf("leaks = %+v, want the observed Makefile:3 path line outside the typed channel", leaks)
	}
}
