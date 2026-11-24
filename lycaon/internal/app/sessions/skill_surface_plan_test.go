package sessions

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func TestSkillToolReaderPreservesRuntimePlan(t *testing.T) {
	for _, tc := range []struct {
		plan        toolsurface.Plan
		wantOffered []string
		wantReq     []string
	}{
		{plan: toolsurface.Compile(nil, nil)},
		{
			plan:        toolsurface.Compile([]string{"skills_read"}, []string{"fetch_url"}),
			wantOffered: []string{"skills_read"},
			wantReq:     []string{"fetch_url"},
		},
	} {
		reader, err := skillToolReader(t.Context(), nil, tools.ToolContext{
			Identity: tools.InvocationIdentity{Agent: "coordinator"},
			Turn:     tools.InvocationTurn{TurnToolPlan: tc.plan},
		})
		testutil.FailErr(t, "skillToolReader", err)
		if !slices.Equal(reader.Offered(), tc.wantOffered) || !slices.Equal(reader.Requestable(), tc.wantReq) {
			t.Fatalf("skillToolReader offered=%v requestable=%v, want %v / %v",
				reader.Offered(), reader.Requestable(), tc.wantOffered, tc.wantReq)
		}
	}

	workerPlan := toolsurface.Compile([]string{"command", "request_tools", "capture_page"}, []string{"fetch_url", "web_search"})
	workerReader, err := skillToolReader(t.Context(), nil, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "implementer"},
		Turn: tools.InvocationTurn{
			TurnToolPlan:         workerPlan,
			TurnOfferedToolNames: []string{"command", "request_tools", "capture_page"},
		},
	})
	testutil.FailErr(t, "worker skillToolReader partial load", err)
	if !slices.Contains(workerReader.Offered(), "capture_page") || slices.Contains(workerReader.Offered(), "fetch_url") {
		t.Fatalf("worker partial-load Offered() = %v", workerReader.Offered())
	}
	if !slices.Contains(workerReader.Requestable(), "fetch_url") || slices.Contains(workerReader.Requestable(), "capture_page") {
		t.Fatalf("worker partial-load Requestable() = %v", workerReader.Requestable())
	}
}
