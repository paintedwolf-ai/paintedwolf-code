package evidence_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMaterialize_readBodyLines(t *testing.T) {
	readJSON := `{"path":"pkg/main.go","content":"1| package main\n2| func main() {}\n","offset":1,"end_line":2,"limit":2}`
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "pkg/main.go", "offset": 1, "limit": 2}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: readJSON}},
	})
	lines := evidence.Materialize(ev, evidence.Span{
		Path:       "pkg/main.go",
		LineRanges: []evidence.LineRange{{Start: 1, End: 1}},
	})
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("materialize = %#v", lines)
	}
}
