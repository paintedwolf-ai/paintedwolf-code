package promptloop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOps8ToolResultPolicyControlsEveryDeliveredCapture(t *testing.T) {
	for _, block := range []bool{false, true} {
		name := "transform"
		if block {
			name = "block"
		}
		t.Run(name, func(t *testing.T) {
			reg := tools.NewStubRegistry()
			testutil.FailErr(t, "register captured tool", reg.Register("recall", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				return "unreviewed-secret", nil
			}))
			seen := 0
			loop := NewPromptLoopForTest(PromptLoopDeps{
				Context: ContextDeps{
					Tools: reg,
				},
				Closeout: CloseoutDeps{
					EvaluateContentAnchor: func(_ context.Context, _ *api.Session, anchor string, observation oar.ContentObservation, tool string, args map[string]any) (*guidance.Refusal, bool, string, bool) {
						seen++
						if anchor != oar.AnchorContentToolResult || tool != "recall" || args["query"] != "observed-input" || len(observation.Segments) != 1 || !strings.Contains(observation.Segments[0].Content, "unreviewed-secret") {
							t.Fatalf("[OAR-FACT-21] incomplete result occurrence: anchor=%s tool=%s args=%v segments=%v", anchor, tool, args, observation.Segments)
						}
						if block {
							return guidance.NewRefusal("RESULT_POLICY", "Result withheld"), true, "", false
						}
						return nil, false, "reviewed replacement", true
					},
				},
			})
			out := loop.Batch.executeOneToolCall(t.Context(), &api.Session{ID: "session"}, "session", "", nil,
				api.ToolCall{ID: "call", Name: "recall", Args: map[string]any{"query": "observed-input"}},
				tools.ToolContext{
					Identity: tools.InvocationIdentity{SessionID: "session",
						Agent: "coordinator"},
				}, nil, "", api.CoordinatorRunContext{}, false)
			testutil.FailErr(t, "execute tool delivery", out.endTurn)
			encoded, err := json.Marshal(out.toolMsg)
			testutil.FailErr(t, "encode delivered result", err)
			if seen != 1 || strings.Contains(string(encoded), "unreviewed-secret") {
				t.Fatalf("[OAR-OPS-8] source escaped result policy: seen=%d result=%s", seen, encoded)
			}
			if !block && !strings.Contains(out.toolMsg.Content, "reviewed replacement") {
				t.Fatalf("[OAR-OPS-8] replacement was not delivered: %s", encoded)
			}
		})
	}
}
