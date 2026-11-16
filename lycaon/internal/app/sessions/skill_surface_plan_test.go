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
}
