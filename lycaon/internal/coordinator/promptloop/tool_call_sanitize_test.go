package promptloop

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSanitizeToolCallsForExecutionDropsEmptyArgPhantom(t *testing.T) {
	valid := api.ToolCall{
		ID:   "call_valid",
		Name: "task",
		Args: taskCallArgs("implementer", "work"),
	}
	phantom := api.ToolCall{
		ID:   "call_phantom",
		Name: "task",
		Args: nil,
		ExtraContent: map[string]any{
			"google": map[string]any{"thought_signature": "sig-from-phantom"},
		},
	}

	got := sanitizeToolCallsForExecution([]api.ToolCall{valid, phantom})
	if len(got) != 1 {
		t.Fatalf("calls = %d want 1", len(got))
	}
	if got[0].ID != valid.ID {
		t.Fatalf("kept call id = %q want %q", got[0].ID, valid.ID)
	}
	if toolCallThoughtSignature(got[0]) != "sig-from-phantom" {
		t.Fatalf("thought_signature = %q want sig migrated from phantom", toolCallThoughtSignature(got[0]))
	}
}

func TestSanitizeToolCallsForExecutionDedupesIdenticalCalls(t *testing.T) {
	args := map[string]any{"path": "a.go"}
	first := api.ToolCall{ID: "tc1", Name: "read", Args: args}
	dup := api.ToolCall{ID: "tc2", Name: "read", Args: map[string]any{"path": "a.go"}}

	got := sanitizeToolCallsForExecution([]api.ToolCall{first, dup})
	if len(got) != 1 {
		t.Fatalf("calls = %d want 1", len(got))
	}
	if got[0].ID != first.ID {
		t.Fatalf("kept call id = %q want first duplicate", got[0].ID)
	}
}

func TestSanitizeToolCallsForExecutionKeepsTruncatedCall(t *testing.T) {
	truncated := api.ToolCall{ID: "tc1", Name: "write", Args: nil, ArgsTruncated: true}
	got := sanitizeToolCallsForExecution([]api.ToolCall{truncated})
	if len(got) != 1 {
		t.Fatalf("calls = %d want truncated call kept", len(got))
	}
}

func TestSanitizeToolCallsForExecutionKeepsMalformedCall(t *testing.T) {
	// The executor returns structured feedback for malformed arguments.
	malformed := api.ToolCall{ID: "tc1", Name: "summarize", Args: nil, ArgsMalformed: true}
	sibling := api.ToolCall{ID: "tc2", Name: "summarize", Args: map[string]any{"task": "t", "path": "README.md"}}
	got := sanitizeToolCallsForExecution([]api.ToolCall{malformed, sibling})
	var keptMalformed bool
	for _, tc := range got {
		if tc.ID == "tc1" && tc.ArgsMalformed {
			keptMalformed = true
		}
	}
	if !keptMalformed {
		t.Fatalf("malformed call dropped by sanitize: %+v", got)
	}
}

func TestSanitizeToolCallsForExecutionKeepsLoneEmptyCall(t *testing.T) {
	got := sanitizeToolCallsForExecution([]api.ToolCall{
		{ID: "tc1", Name: "task", Args: map[string]any{}},
	})
	if len(got) != 1 {
		t.Fatalf("calls = %d want lone empty call kept for reject path", len(got))
	}
}

func TestToolCallHasExecutableArgs(t *testing.T) {
	if !toolCallHasExecutableArgs(map[string]any{"prompt": "go"}) {
		t.Fatal("non-empty string arg should be executable")
	}
	if toolCallHasExecutableArgs(map[string]any{"prompt": "  "}) {
		t.Fatal("whitespace-only string should not be executable")
	}
	if toolCallHasExecutableArgs(map[string]any{"scope": map[string]any{}}) {
		t.Fatal("empty nested object should not be executable")
	}
}

func TestPatchAssistantToolCalls(t *testing.T) {
	history := []api.Message{
		{ID: "a1", Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "old"}}},
	}
	sanitized := []api.ToolCall{{ID: "new", Name: "read", Args: map[string]any{"path": "x"}}}
	out := patchAssistantToolCalls(history, "a1", sanitized)
	if len(out[0].ToolCalls) != 1 || out[0].ToolCalls[0].ID != "new" {
		t.Fatalf("patched tool_calls = %+v", out[0].ToolCalls)
	}
}

func TestOfferedToolNamesEmptyIsStamped(t *testing.T) {
	got := offeredToolNames(nil)
	if got == nil {
		t.Fatal("empty offer must stamp a non-nil slice so prose-only turns filter")
	}
	if len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestEmptyToolOfferReplacesPriorTurnAllowlist(t *testing.T) {
	st := &promptLoopTurnState{offeredToolNames: []string{"read"}}
	st.stampOfferedToolNames(nil)
	if st.offeredToolNames == nil || len(st.offeredToolNames) != 0 {
		t.Fatalf("offered tools = %v, want stamped empty allowlist", st.offeredToolNames)
	}
}

func TestRetainOfferedToolCallsDropsUnoffered(t *testing.T) {
	calls := []api.ToolCall{
		{ID: "tc1", Name: "read", Args: map[string]any{"path": "x.go"}},
		{ID: "tc2", Name: "update_progress", Args: map[string]any{"content": "x"}},
	}
	got := retainOfferedToolCalls(nil, "a1", calls, []string{"read"})
	if len(got) != 1 || got[0].ID != "tc1" {
		t.Fatalf("kept = %+v want only read", got)
	}
}

func TestRetainOfferedToolCallsEmptyOfferKeepsAnsweredOnly(t *testing.T) {
	calls := []api.ToolCall{
		{ID: "tc1", Name: "read"},
		{ID: "tc2", Name: "update_progress"},
	}
	history := []api.Message{{
		Role:       api.MessageRoleTool,
		ToolResult: &api.ToolResult{AssistantMessageID: "a1", ToolCallID: "tc1"},
	}}
	got := retainOfferedToolCalls(history, "a1", calls, []string{})
	if len(got) != 1 || got[0].ID != "tc1" {
		t.Fatalf("empty offer must keep answered only, got %+v", got)
	}
}

func TestRetainOfferedToolCallsNilOfferKeepsAll(t *testing.T) {
	calls := []api.ToolCall{
		{ID: "tc1", Name: "read"},
		{ID: "tc2", Name: "update_progress"},
	}
	got := retainOfferedToolCalls(nil, "a1", calls, nil)
	if len(got) != 2 {
		t.Fatalf("nil offer must leave calls unchanged, got %+v", got)
	}
}

func TestSanitizeToolCallsForExecutionPreservesParallelDistinctTasks(t *testing.T) {
	calls := []api.ToolCall{
		{ID: "tc1", Name: "task", Args: taskCallArgs("a", "one")},
		{ID: "tc2", Name: "task", Args: taskCallArgs("b", "two")},
	}
	got := sanitizeToolCallsForExecution(calls)
	if len(got) != 2 {
		t.Fatalf("calls = %d want 2 distinct tasks", len(got))
	}
	first := got[0].Args["brief"].(map[string]any)["goal"].(string)
	second := got[1].Args["brief"].(map[string]any)["goal"].(string)
	if strings.TrimSpace(first) == strings.TrimSpace(second) {
		t.Fatal("expected distinct task goals")
	}
}

func TestOfferedArgumentSchemasStayBoundToModelRequest(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"brief": map[string]any{"type": "object"}},
	}
	st := &promptLoopTurnState{}
	st.stampOfferedToolNames([]tools.ToolMeta{{Name: "task", ArgsSchema: schema}})
	schema["properties"].(map[string]any)["brief"].(map[string]any)["type"] = "string"
	invalid := map[string]any{"brief": "serialized charter"}
	if tools.ValidateToolArgs(st.offeredToolSchemas["task"], invalid) == nil {
		t.Fatal("changing registry metadata changed the request's argument schema")
	}
	st.stampOfferedToolNames(nil)
	if len(st.offeredToolSchemas) != 0 {
		t.Fatal("a new empty offer retained the previous request's schema")
	}
}

func TestTextActionRecoveryThroughLoop(t *testing.T) {
	newPipeline := outputPolicyPipeline(t)
	for _, tc := range []struct {
		name, text           string
		repeats              int
		native               bool
		wantReject, wantExec int
	}{
		{"worker resubmit", `{"tool_name":"read","tool_args":{"path":"x"}}`, 1, false, 1, 1},
		{"resubmit", `{"tool_name":"read","tool_args":{"path":"x"}}`, 1, false, 1, 1},
		{"prefix resubmit", `{"name":"read","arguments":"bad"} Reading now.`, 1, false, 1, 1},
		{"name arguments repair", `{"name":"read","arguments":{"path":"x"}}`, 1, false, 1, 1},
		{"native unchanged", `{"tool_name":"read","tool_args":{}}`, 1, true, 0, 1},
		{"fenced example", "```json\n{\"tool_name\":\"read\",\"tool_args\":{}}\n```", 1, false, 0, 0},
		{"fenced name arguments", "```json\n{\"name\":\"read\",\"arguments\":{\"path\":\"x\"}}\n```", 1, false, 0, 0},
		{"explained tag", `Example: <tool_call>{"name":"read","arguments":{"path":"x"}}</tool_call>`, 1, false, 0, 0},
		{"bounded retry", `{"tool":"read","args":{}}`, 20, false, BlockedLoopRejectCap + 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := tools.NewStubRegistry()
			executed, rejected, nudges := 0, 0, 0
			testutil.FailErr(t, "register", reg.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				executed++
				return "file-body", nil
			}))
			native := []api.ToolCall{{ID: "native", Name: "read", Args: map[string]any{"path": "x"}}}
			responses := make([]*modelcall.Completion, tc.repeats)
			for i := range responses {
				responses[i] = &modelcall.Completion{Content: tc.text}
				if tc.native {
					responses[i].ToolCalls = native
				}
			}
			responses = append(responses, &modelcall.Completion{ToolCalls: native}, &modelcall.Completion{Content: "done"})
			if tc.wantReject == 0 {
				responses = responses[:1]
				responses = append(responses, &modelcall.Completion{Content: "done"})
			}
			deps := PromptLoopDeps{
				Context: ContextDeps{Tools: reg, Limits: func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() }, BuildMessages: func(_ context.Context, _ *api.Session, h []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
					return h, nil
				}},
				Model:    ModelDeps{LLM: &textActionClient{responses: responses}},
				Closeout: CloseoutDeps{EvaluateContentAnchor: outputPolicyEvaluator(t, newPipeline())},
				Projection: ProjectionDeps{
					AppendDraftVersion: func(_ context.Context, _, _, body, code string) (int, error) {
						if code == "TOOL_ACTION_IN_TEXT" {
							rejected++
							if body != tc.text {
								t.Fatalf("lost rejected draft: %q", body)
							}
						}
						return rejected, nil
					},
					AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
						for _, msg := range msgs {
							if strings.Contains(msg.Content, "Code: TOOL_ACTION_IN_TEXT") {
								nudges++
								if !strings.Contains(msg.Content, "native tool channel") {
									t.Fatal("missing recovery instruction")
								}
							}
						}
						return nil
					},
				},
			}
			loop := NewPromptLoopForTest(deps)
			sess := &api.Session{ID: t.Name(), Posture: api.SessionPostureBuild}
			if tc.name == "worker resubmit" {
				sess.ParentSessionID = "parent"
				sess.AgentType = "path-explorer"
			}
			_, err := loop.Run(t.Context(), PromptRunInput{SessionID: sess.ID, Session: sess, History: []api.Message{{Role: api.MessageRoleUser, Content: "Read x"}}, ProfileID: "explore_readonly", ToolCtx: tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sess.ID}}})
			testutil.FailErr(t, "run recovery", err)
			if rejected != tc.wantReject || executed != tc.wantExec || (rejected > 0 && nudges == 0) {
				t.Fatalf("rejected=%d nudges=%d executed=%d; want rejected=%d executed=%d", rejected, nudges, executed, tc.wantReject, tc.wantExec)
			}
		})
	}
}

type textActionClient struct {
	responses []*modelcall.Completion
	calls     int
}

func (c *textActionClient) Complete(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if c.calls >= len(c.responses) {
		return &modelcall.Completion{Content: "done"}, nil
	}
	response := *c.responses[c.calls]
	c.calls++
	return &response, nil
}
func (c *textActionClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	response, err := c.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: response.Content, ToolCalls: response.ToolCalls, Done: true}
	close(ch)
	return ch, nil
}
