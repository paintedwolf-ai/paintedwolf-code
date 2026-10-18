package promptloop

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWaitVisibilityAtBatchBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, text           string
		note, failNote, park bool
	}{
		{"hidden answer", "The build is still running", false, false, false},
		{"silent wait", "", false, false, true},
		{"note and wait", "", true, false, true},
		{"note covers text", "The build is still running", true, false, true},
		{"rejected note", "The build is still running", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := tools.NewStubRegistry()
			waitRuns := 0
			testutil.FailErr(t, "register wait", reg.Register("wait", func(_ context.Context, _ map[string]any, ctx tools.ToolContext) (string, error) {
				waitRuns++
				ctx.Effects.Out.Completion = &api.ToolCompletion{Operation: "wait", State: "parked"}
				return `{"status":"sleeping"}`, nil
			}))
			testutil.FailErr(t, "register note", reg.Register("surface_note", func(_ context.Context, _ map[string]any, ctx tools.ToolContext) (string, error) {
				if tc.failNote {
					return "", guidance.NewRefusal("TEST_NOTE_REJECTED", "")
				}
				ctx.Effects.Out.AgentNote = &tools.AgentNoteCapture{MessageID: "note-1", Content: "Build running", Grounding: &api.CitationGrounding{Traced: true}}
				return `{"status":"noted"}`, nil
			}))
			loop := NewPromptLoopForTest(PromptLoopDeps{
				Context:    ContextDeps{Tools: reg},
				Projection: ProjectionDeps{AppendMessages: func(context.Context, string, ...api.Message) error { return nil }},
				Tools: ToolsDeps{BeforeToolRun: func(_ context.Context, sess *api.Session, history []api.Message, _, tool string, _ map[string]any) (string, bool, error) {
					gc := oar.NewGuardContext()
					guard.ObserveWaitVisibility(sess, history, tool, true, gc)
					if guard.EvaluateObserveHasCode(gc, oar.AnchorCoordinatorPreInvoke, guard.WaitHiddenTextCode) {
						return "", false, guidance.NewRefusal(guard.WaitHiddenTextCode, "")
					}
					return "", false, nil
				}},
			})
			// Deliberately request wait first; execution must commit the note first.
			calls := []api.ToolCall{{ID: "wait-1", Name: "wait", Args: map[string]any{"timeout_ms": 60000}}}
			if tc.note {
				calls = append(calls, api.ToolCall{ID: "note-1", Name: "surface_note", Args: map[string]any{"summary": "Build running"}})
			}
			sess := &api.Session{ID: "coordinator", Posture: api.SessionPostureBuild}
			history := []api.Message{{ID: "assistant-1", Role: api.MessageRoleAssistant, Content: tc.text, ToolCalls: calls}}
			history, _, _, _, _, parked, err := loop.Batch.executeToolCallsInTurn(context.Background(), sess, sess.ID, calls,
				tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sess.ID, Agent: "coordinator"}}, history, "investigate", "assistant-1", "", nil)
			testutil.FailErr(t, "execute wait batch", err)
			if parked != tc.park || (waitRuns == 1) != tc.park {
				t.Fatalf("parked=%v, wait invocations=%d, want park=%v", parked, waitRuns, tc.park)
			}
			if !tc.park {
				found := false
				for _, msg := range history {
					if msg.ToolResult != nil && msg.ToolResult.Tool == "wait" && msg.ToolResult.Outcome == api.ToolResultOutcomeRejected {
						found = true
					}
				}
				if !found {
					t.Fatal("blocked wait must retain a rejected result")
				}
			}
		})
	}
}
