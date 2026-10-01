package evidence_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/pkg/api"
)

// goldenFixtureMessages exercise stable grounding output.
var goldenFixtureMessages = []api.Message{
	{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
		{Name: "read", ID: "c1", Args: map[string]any{"path": "pkg/main.go", "offset": 1, "limit": 2}},
		{Name: "grep", ID: "c2", Args: map[string]any{"path": ".", "pattern": "main"}},
	}},
	{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
		Outcome: api.ToolResultOutcomeCompleted,
		Content: `{"path":"pkg/main.go","content":"1| package main\n2| func main() {}\n","offset":1,"end_line":2,"limit":2}`,
	}},
	{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
		Outcome: api.ToolResultOutcomeCompleted,
		Content: `{"matches":[{"path":"pkg/main.go","line":1,"content":"package main"}],"receipt":{"tool":"grep"}}`,
	}},
}

var goldenExpectedHandles = []string{"grep#1", "read#1"}

var goldenResolveCases = []struct {
	triple evidence.Triple
	handle string
	want   evidence.Verdict
}{
	{evidence.Triple{Path: "pkg/main.go", Line: 1, Excerpt: "package main"}, "read#1", evidence.VerdictMatched},
	{evidence.Triple{Path: "pkg/main.go", Line: 1, Excerpt: "package entrypoint"}, "read#1", evidence.VerdictTraced},
	{evidence.Triple{Path: "missing.go", Line: 1, Excerpt: "package missing"}, "", evidence.VerdictUnverifiable},
}

func TestGroundingSubstrateGolden(t *testing.T) {
	ev := ledgertest.BuildFromMessages("", goldenFixtureMessages)

	gotHandles := evidence.HandlesSorted(ev)
	if len(gotHandles) != len(goldenExpectedHandles) {
		t.Fatalf("handles = %v want %v", gotHandles, goldenExpectedHandles)
	}
	for i, want := range goldenExpectedHandles {
		if gotHandles[i] != want {
			t.Fatalf("handles = %v want %v", gotHandles, goldenExpectedHandles)
		}
	}
	if !evidence.PathObserved(ev, "pkg/main.go") {
		t.Fatalf("paths = %v", evidence.ObservedPathsSorted(ev))
	}

	for i, tc := range goldenResolveCases {
		got := evidence.Resolve(evidence.CitationRoots{}, tc.triple, ev, tc.handle)
		if got.Verdict != tc.want {
			t.Fatalf("case %d verdict = %q want %q", i, got.Verdict, tc.want)
		}
		if tc.handle != "" && got.Handle != tc.handle {
			t.Fatalf("case %d handle = %q want %q", i, got.Handle, tc.handle)
		}
	}
}
