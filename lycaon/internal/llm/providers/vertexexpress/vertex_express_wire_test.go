package vertexexpress

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// System messages become system instructions outside the turn list.
func TestVertexExpressWireSplitsSystemInstruction(t *testing.T) {
	system, contents := ProjectMessages([]api.Message{
		{Role: api.MessageRoleSystem, Content: "be terse"},
		{Role: api.MessageRoleSystem, Content: "and correct"},
		{Role: api.MessageRoleUser, Content: "hi"},
	}, false, "")

	if system == nil {
		t.Fatal("systemInstruction missing")
	}
	if len(system.Parts) != 2 {
		t.Fatalf("system parts = %d, want 2 (kept separate, not concatenated)", len(system.Parts))
	}
	if system.Role != "" {
		t.Errorf("systemInstruction role = %q, want empty", system.Role)
	}
	if len(contents) != 1 || contents[0].Role != "user" {
		t.Fatalf("contents = %+v, want a single user turn", contents)
	}
}

// Blank system messages contribute nothing rather than an empty part.
func TestVertexExpressWireSkipsBlankSystem(t *testing.T) {
	system, _ := ProjectMessages([]api.Message{
		{Role: api.MessageRoleSystem, Content: "   "},
		{Role: api.MessageRoleUser, Content: "hi"},
	}, false, "")
	if system != nil {
		t.Fatalf("systemInstruction = %+v, want nil for whitespace-only system text", system)
	}
}

// A tool result pairs with its preceding call by name in a user turn.
func TestVertexExpressWireToolResultPairsByName(t *testing.T) {
	_, contents := ProjectMessages([]api.Message{
		{Role: api.MessageRoleUser, Content: "read it"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{
			ID: "host-1", Name: "read", Args: map[string]any{"path": "main.go"},
		}}},
		{Role: api.MessageRoleTool, Content: "package main", ToolResult: &api.ToolResult{Tool: "read"}},
	}, false, "")

	if len(contents) != 3 {
		t.Fatalf("contents = %d turns, want 3", len(contents))
	}
	call := contents[1].Parts[0].FunctionCall
	if contents[1].Role != "model" || call == nil || call.Name != "read" {
		t.Fatalf("turn 1 = %+v, want a model functionCall for read", contents[1])
	}
	resp := contents[2].Parts[0].FunctionResponse
	if contents[2].Role != "user" || resp == nil {
		t.Fatalf("turn 2 = %+v, want a user functionResponse", contents[2])
	}
	if resp.Name != "read" {
		t.Errorf("functionResponse name = %q, want read (pairs the call by name)", resp.Name)
	}
	if got := resp.Response[vertexExpressToolResultKey]; got != "package main" {
		t.Errorf("response[%s] = %v, want the tool output", vertexExpressToolResultKey, got)
	}
}

// Parallel results keep the identity stamped on each result, independent of call order.
func TestVertexExpressWireParallelToolResultsUseResultIdentity(t *testing.T) {
	_, contents := ProjectMessages([]api.Message{
		{Role: api.MessageRoleUser, Content: "read both"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{ID: "h1", Name: "read", Args: map[string]any{"path": "a.go"}},
			{ID: "h2", Name: "grep", Args: map[string]any{"q": "func"}},
		}},
		{Role: api.MessageRoleTool, Content: "grep hits", ToolResult: &api.ToolResult{Tool: "grep"}},
		{Role: api.MessageRoleTool, Content: "a contents", ToolResult: &api.ToolResult{Tool: "read"}},
	}, false, "")

	last := contents[len(contents)-1]
	if len(last.Parts) != 2 {
		t.Fatalf("tool-result turn parts = %d, want both results merged into one user turn", len(last.Parts))
	}
	if got := last.Parts[0].FunctionResponse.Name; got != "grep" {
		t.Errorf("first result name = %q, want grep", got)
	}
	if got := last.Parts[1].FunctionResponse.Name; got != "read" {
		t.Errorf("second result name = %q, want read", got)
	}
}

func TestVertexExpressWireDropsOrphanToolResult(t *testing.T) {
	_, contents := ProjectMessages([]api.Message{
		{Role: api.MessageRoleTool, Content: "stray output"},
	}, false, "")

	if len(contents) != 0 {
		t.Fatalf("contents = %+v, want orphan dropped", contents)
	}
}

func TestVertexExpressWireDropsNamedResultWithoutPendingCall(t *testing.T) {
	_, contents := ProjectMessages([]api.Message{{
		Role: api.MessageRoleTool, Content: "stray output", ToolResult: &api.ToolResult{Tool: "read"},
	}}, false, "")
	if len(contents) != 0 {
		t.Fatalf("contents = %+v, want named orphan dropped", contents)
	}
}

// Consecutive same-role messages merge to preserve alternating turns.
func TestVertexExpressWireMergesConsecutiveRoles(t *testing.T) {
	_, contents := ProjectMessages([]api.Message{
		{Role: api.MessageRoleUser, Content: "one"},
		{Role: api.MessageRoleUser, Content: "two"},
	}, false, "")

	if len(contents) != 1 {
		t.Fatalf("contents = %d turns, want 1 merged user turn", len(contents))
	}
	if len(contents[0].Parts) != 2 {
		t.Fatalf("parts = %d, want both messages kept as separate parts", len(contents[0].Parts))
	}
}

// Every declaration goes in one tool object, not one tool per function.
func TestVertexExpressToolsFromHostSingleToolObject(t *testing.T) {
	got := ProjectTools([]tools.ToolMeta{
		{Name: "read", Description: "read a file", ArgsSchema: map[string]any{"type": "object"}},
		{Name: "grep", Description: "search"},
	})
	if len(got) != 1 {
		t.Fatalf("tools = %d, want 1 object holding every declaration", len(got))
	}
	if len(got[0].FunctionDeclarations) != 2 {
		t.Fatalf("declarations = %d, want 2", len(got[0].FunctionDeclarations))
	}
	if got[0].FunctionDeclarations[0].Name != "read" {
		t.Errorf("first declaration = %q", got[0].FunctionDeclarations[0].Name)
	}
}

func TestVertexExpressToolsFromHostEmpty(t *testing.T) {
	if got := ProjectTools(nil); got != nil {
		t.Fatalf("tools = %+v, want nil so the field is omitted", got)
	}
}

// promptTokenCount already includes cached content, so it passes through as the
// inclusive total; thought tokens bill as output.
func TestTokenUsageFromVertexExpress(t *testing.T) {
	got := tokenUsageFromVertexExpress(&vertexExpressUsageMetadata{
		PromptTokenCount:     100,
		CandidatesTokenCount: 20,
		ThoughtsTokenCount:   30,
		CachedContentTokens:  40,
	})
	want := modelcall.TokenUsage{
		Present:              true,
		PromptTokens:         100,
		CompletionTokens:     50,
		CacheReadInputTokens: 40,
	}
	if got != want {
		t.Fatalf("usage = %+v, want %+v", got, want)
	}
}

func TestTokenUsageFromVertexExpressNil(t *testing.T) {
	if got := tokenUsageFromVertexExpress(nil); got != (modelcall.TokenUsage{}) {
		t.Fatalf("usage = %+v, want zero", got)
	}
}

func TestMapVertexExpressResponseSplitsThoughtFromContent(t *testing.T) {
	got := mapVertexExpressResponse(&vertexExpressResponse{
		Candidates: []vertexExpressCandidate{{
			Content: vertexExpressContent{Parts: []vertexExpressPart{
				{Text: "reasoning here", Thought: true},
				{Text: "answer"},
				{FunctionCall: &vertexExpressFunctionCall{Name: "read", Args: map[string]any{"path": "x"}}},
			}},
			FinishReason: "STOP",
		}},
		UsageMetadata: &vertexExpressUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 7},
	})
	if got.Content != "answer" {
		t.Errorf("content = %q, want the thought excluded", got.Content)
	}
	if got.Reasoning != "reasoning here" {
		t.Errorf("reasoning = %q", got.Reasoning)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "read" {
		t.Errorf("tool calls = %+v", got.ToolCalls)
	}
}

// Content filters return 200 with an empty candidate and the cause only in
// finishReason. Every accepted empty turn is an error rather than silence.
func TestVertexExpressEmptyOutputErr(t *testing.T) {
	cases := []struct {
		name         string
		finishReason string
		hasOutput    bool
		wantErr      bool
		wantRetry    bool
	}{
		{"safety block", "SAFETY", false, true, false},
		{"recitation block", "RECITATION", false, true, false},
		{"blocklist", "BLOCKLIST", false, true, false},
		{"prohibited content", "PROHIBITED_CONTENT", false, true, false},
		{"malformed function call", "MALFORMED_FUNCTION_CALL", false, true, false},
		// Not an allowlist: a reason Google adds later is reported too.
		{"unknown future reason", "SOME_NEW_REASON", false, true, false},
		// The cap was hit before a single token of output — silence would hide it.
		{"max tokens with nothing", "MAX_TOKENS", false, true, false},
		// The ordinary truncation case: real output, cap hit. Not an error.
		{"max tokens mid answer", "MAX_TOKENS", true, false, false},
		{"safety flagged but output present", "SAFETY", true, false, false},
		{"clean stop, no output", "STOP", false, true, true},
		{"clean stop with output", "STOP", true, false, false},
		{"absent reason", "", false, true, true},
		{"whitespace reason", "   ", false, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := vertexExpressEmptyOutputErr("vertex", "model", tc.finishReason, tc.hasOutput)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), strings.TrimSpace(tc.finishReason)) {
				t.Errorf("error %q must name the finishReason %q", err, tc.finishReason)
			}
			if err != nil {
				empty, ok := failure.AsProviderEmptyCompletion(err)
				if !ok {
					t.Fatalf("error %T is not ProviderEmptyCompletionError", err)
				}
				if empty.Retryable != tc.wantRetry {
					t.Errorf("Retryable = %v, want %v", empty.Retryable, tc.wantRetry)
				}
			}
		})
	}
}

func TestMapVertexExpressResponseNoCandidates(t *testing.T) {
	got := mapVertexExpressResponse(&vertexExpressResponse{})
	if got == nil {
		t.Fatal("want a Completion, not nil")
	}
	if got.Content != "" || len(got.ToolCalls) != 0 {
		t.Fatalf("completion = %+v, want empty", got)
	}
}
