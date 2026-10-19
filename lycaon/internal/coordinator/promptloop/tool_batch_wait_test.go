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

func TestReorderToolBatchExecutionPutsTerminalToolsLast(t *testing.T) {
	calls := []api.ToolCall{
		{Name: "wait", Args: map[string]any{"timeout_ms": 60_000}},
		{Name: "read", Args: map[string]any{"path": "x.go"}},
	}
	got := reorderToolBatchExecution(batchTestRegistry(t, "wait", "read"), calls)
	if len(got) != 2 || got[0].Name != "read" || got[1].Name != "wait" {
		t.Fatalf("got = %+v", got)
	}
}

func TestSanitizeToolCallsForExecutionPreservesRepeatableStatefulCalls(t *testing.T) {
	calls := []api.ToolCall{
		{ID: "ts1", Name: "terminal_send", Args: map[string]any{"session_id": "term-1", "keys": []any{"Down"}}},
		{ID: "ts2", Name: "terminal_send", Args: map[string]any{"session_id": "term-1", "keys": []any{"Down"}}},
		{ID: "ts2", Name: "terminal_send", Args: map[string]any{"session_id": "term-1", "keys": []any{"Down"}}},
		{ID: "pa1", Name: "page_act", Args: map[string]any{"page_id": "p1", "action": "press", "key": "ArrowDown"}},
		{ID: "pa2", Name: "page_act", Args: map[string]any{"page_id": "p1", "action": "press", "key": "ArrowDown"}},
		{ID: "w1", Name: "write", Args: map[string]any{"path": "log.txt", "content": "line\n", "append": true}},
		{ID: "w2", Name: "write", Args: map[string]any{"path": "log.txt", "content": "line\n", "append": true}},
		{ID: "wo1", Name: "write", Args: map[string]any{"path": "out.txt", "content": "same"}},
		{ID: "wo2", Name: "write", Args: map[string]any{"path": "out.txt", "content": "same"}},
	}
	got := sanitizeToolCallsForExecution(calls)
	if len(got) != 7 {
		t.Fatalf("calls = %d want 7 (distinct stateful calls kept, duplicate ID ts2 and overwrite wo2 dropped): %+v", len(got), got)
	}
}

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
