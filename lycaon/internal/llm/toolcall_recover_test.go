package llm

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/tools"
)

func toolReq() modelcall.CompletionRequest {
	return modelcall.CompletionRequest{ToolProtocol: modelcall.ToolProtocolEnvelope, Tools: []tools.ToolMeta{{Name: "read"}}}
}

func TestClassifyToolResponseRecoversProse(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantName string
		wantArg  string
	}{
		{
			name:     "tool_call tags",
			content:  `<tool_call>{"name": "read", "arguments": {"path": "main.go"}}</tool_call>`,
			wantName: "read",
			wantArg:  "main.go",
		},
		{
			name:     "mistral prefix",
			content:  `[TOOL_CALLS][{"name": "read", "arguments": {"path": "z.go"}}]`,
			wantName: "read",
			wantArg:  "z.go",
		},
		{
			name:     "string-encoded arguments",
			content:  `{"name": "read", "arguments": "{\"path\": \"q.md\"}"}`,
			wantName: "read",
			wantArg:  "q.md",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &modelcall.Completion{Content: tc.content}
			out, err := ClassifyToolResponse(providerprofile.OpenAI(), toolReq(), c, "p", "m")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(out.Completion.ToolCalls) != 1 {
				t.Fatalf("recovered %d tool calls, want 1", len(out.Completion.ToolCalls))
			}
			if out.Completion.ToolCalls[0].Name != tc.wantName {
				t.Errorf("name = %q, want %q", out.Completion.ToolCalls[0].Name, tc.wantName)
			}
			if got, _ := out.Completion.ToolCalls[0].Args["path"].(string); got != tc.wantArg {
				t.Errorf("args path = %q, want %q", got, tc.wantArg)
			}
			if out.Completion.Content != "" {
				t.Errorf("content not blanked after recovery: %q", out.Completion.Content)
			}
		})
	}
}

func TestClassifyToolResponseLeavesProseAnswer(t *testing.T) {
	c := &modelcall.Completion{Content: "Here is a summary of the changes you asked about."}
	out, err := ClassifyToolResponse(providerprofile.OpenAI(), toolReq(), c, "p", "m")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Completion.ToolCalls) != 0 || out.Completion.Content == "" {
		t.Fatalf("prose answer should pass through untouched, got %#v", out)
	}
}

func TestClassifyToolResponseUnparseableErrors(t *testing.T) {
	content := `<tool_call>{name: read, arguments: not-json}</tool_call>`

	if _, err := ClassifyToolResponse(providerprofile.OpenAI(), toolReq(), &modelcall.Completion{Content: content}, "p", "m"); err == nil {
		t.Fatal("expected ProviderToolCallsInProse error for unparseable attempt")
	} else if _, ok := failure.AsProviderToolCallsInProse(err); !ok {
		t.Fatalf("error = %v, want ProviderToolCallsInProse", err)
	}

	noToolProfile := providerprofile.OpenAI()
	noToolProfile.ToolCalls = providerprofile.ToolCallsNone
	if _, err := ClassifyToolResponse(noToolProfile, toolReq(), &modelcall.Completion{Content: content}, "p", "m"); err == nil {
		t.Fatal("expected ProviderToolCallsUnsupported error")
	} else if _, ok := failure.AsProviderToolCallsUnsupported(err); !ok {
		t.Fatalf("error = %v, want ProviderToolCallsUnsupported", err)
	}
}

func TestClassifyToolResponseNoopWhenNoTools(t *testing.T) {
	c := &modelcall.Completion{Content: `<tool_call>{"name":"read","arguments":{}}</tool_call>`}
	out, err := ClassifyToolResponse(providerprofile.OpenAI(), modelcall.CompletionRequest{}, c, "p", "m")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Completion.ToolCalls) != 0 {
		t.Fatal("must not recover when the turn offered no tools")
	}
}

func TestClassifyToolResponseRequiresDeclaredTextProtocol(t *testing.T) {
	profile := providerprofile.OpenAI()
	profile.TextToolCallGrammars = 0
	content := `<tool_call>{"name":"read","arguments":{"path":"main.go"}}</tool_call>`
	out, err := ClassifyToolResponse(profile, toolReq(), &modelcall.Completion{Content: content}, "p", "m")
	if err == nil || out != nil {
		t.Fatalf("unsupported selected protocol accepted: %+v %v", out, err)
	}
}

func TestClassifyToolResponseRequiresDeclaredHarmonyProtocol(t *testing.T) {
	content := `assistantcommentary to=functions.read json{"path":"main.go"}`
	profile := providerprofile.OpenAI()
	profile.TextToolCallGrammars = providerprofile.TextToolCallGrammarBoundedEnvelope
	out, err := ClassifyToolResponse(profile, harmonyToolReq(), &modelcall.Completion{Content: content}, "p", "m")
	if err == nil || out != nil {
		t.Fatalf("unsupported selected protocol accepted: %+v %v", out, err)
	}

	profile.TextToolCallGrammars = providerprofile.TextToolCallGrammarHarmony
	out, err = ClassifyToolResponse(profile, harmonyToolReq(), &modelcall.Completion{Content: content}, "p", "m")
	if err != nil {
		t.Fatalf("unexpected declared-protocol error: %v", err)
	}
	if len(out.Completion.ToolCalls) != 1 || out.Completion.ToolCalls[0].Name != "read" {
		t.Fatalf("declared grammar did not recover call: %#v", out)
	}
}

func TestClassifyToolResponseValidatesOfferedToolAndSchema(t *testing.T) {
	req := modelcall.CompletionRequest{ToolProtocol: modelcall.ToolProtocolEnvelope, Tools: []tools.ToolMeta{{
		Name: "read",
		ArgsSchema: map[string]any{
			"type":     "object",
			"required": []string{"path"},
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
		},
	}}}
	for _, content := range []string{
		`<tool_call>{"name":"write","arguments":{"path":"main.go"}}</tool_call>`,
		`<tool_call>{"name":"read","arguments":{"path":7}}</tool_call>`,
	} {
		_, err := ClassifyToolResponse(providerprofile.OpenAI(), req, &modelcall.Completion{Content: content}, "p", "m")
		if _, ok := failure.AsProviderToolCallsInProse(err); !ok {
			t.Fatalf("content %q: error = %v, want validated prose-call rejection", content, err)
		}
	}
}

func TestClassifyToolResponseRejectsMalformedBatch(t *testing.T) {
	valid := `{"name":"read","arguments":{}}`
	for _, invalid := range []string{
		`{"name":"read","arguments":7}`,
		`{"name":"read","arguments":null}`,
		`{"name":"read","arguments":"not-json"}`,
		`{"name":"read","arguments":[],"parameters":{}}`,
		`{"name":"read"}`,
		`{"name":"","arguments":{}}`,
		`not-json`,
	} {
		for _, content := range []string{
			"<tool_call>" + valid + "</tool_call><tool_call>" + invalid + "</tool_call>",
			"<tool_call>" + invalid + "</tool_call><tool_call>" + valid + "</tool_call>",
			"[TOOL_CALLS][" + valid + "," + invalid + "]",
		} {
			c := &modelcall.Completion{Content: content}
			out, err := ClassifyToolResponse(providerprofile.OpenAI(), toolReq(), c, "p", "m")
			if _, ok := failure.AsProviderToolCallsInProse(err); !ok || out != nil {
				t.Fatalf("content %q: got %#v, %v; want batch rejection", content, out, err)
			}
			if len(c.ToolCalls) != 0 || c.Content != content {
				t.Fatalf("rejected batch changed completion: %#v", c)
			}
		}
	}
}

func TestClassifyToolResponseRecoversEmptyArguments(t *testing.T) {
	for _, content := range []string{
		`<tool_call>{"name":"read","arguments":{}}</tool_call>`,
		`<tool_call>{"name":"read","parameters":{}}</tool_call>`,
		`<tool_call>{"name":"read","arguments":"{}"}</tool_call>`,
	} {
		out, err := ClassifyToolResponse(providerprofile.OpenAI(), toolReq(), &modelcall.Completion{Content: content}, "p", "m")
		if err != nil || out == nil || len(out.Completion.ToolCalls) != 1 || out.Completion.ToolCalls[0].Args == nil {
			t.Fatalf("content %q: got %#v, %v; want empty object arguments", content, out, err)
		}
	}
}

func TestClassifyToolResponseValidatesHarmonyCalls(t *testing.T) {
	profile := providerprofile.OpenAI()
	profile.TextToolCallGrammars = providerprofile.TextToolCallGrammarHarmony
	req := harmonyToolReq()
	req.Tools[0].ArgsSchema = map[string]any{"type": "object", "additionalProperties": false}
	for _, content := range []string{
		`assistantcommentary to=functions.write json{}`,
		`assistantcommentary to=functions.read json{"unexpected":true}`,
	} {
		c := &modelcall.Completion{Content: content}
		out, err := ClassifyToolResponse(profile, req, c, "p", "m")
		if _, ok := failure.AsProviderToolCallsInProse(err); !ok || out != nil {
			t.Fatalf("content %q: got %#v, %v; want validated Harmony rejection", content, out, err)
		}
		if len(c.ToolCalls) != 0 || c.Content != content {
			t.Fatalf("rejected Harmony call changed completion: %#v", c)
		}
	}
}

func TestClassifyToolResponseDoesNotPromotePartialHarmony(t *testing.T) {
	profile := providerprofile.OpenAI()
	profile.TextToolCallGrammars = providerprofile.TextToolCallGrammarHarmony
	content := `assistantcommentary to=functions.read json{}assistantcommentary to=functions.read missing-json-header{}`
	out, err := ClassifyToolResponse(profile, harmonyToolReq(), &modelcall.Completion{Content: content}, "p", "m")
	if err != nil || out == nil || len(out.Completion.ToolCalls) != 0 || out.Completion.Content != content {
		t.Fatalf("malformed Harmony promoted partial calls: %#v, %v", out, err)
	}
}

func TestClassifyToolResponseKeepsProseWhenToolUseForbidden(t *testing.T) {
	req := toolReq()
	req.ToolUse = modelcall.ToolUseForbidden
	content := `<tool_call>{"name": "read", "arguments": {"path": "main.go"}}</tool_call>`
	out, err := ClassifyToolResponse(providerprofile.OpenAI(), req, &modelcall.Completion{Content: content}, "p", "m")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Completion.ToolCalls) != 0 || out.Completion.Content != content {
		t.Fatalf("a forbidden turn recovered %d tool calls from prose", len(out.Completion.ToolCalls))
	}
}

func harmonyToolReq() modelcall.CompletionRequest {
	req := toolReq()
	req.ToolProtocol = modelcall.ToolProtocolHarmony
	return req
}

func TestNativeRequestDoesNotExecuteTextExamples(t *testing.T) {
	for _, content := range []string{
		"```json\n{\"name\":\"read\",\"arguments\":{\"path\":\"x\"}}\n```",
		`Example: <tool_call>{"name":"read","arguments":{"path":"x"}}</tool_call>`,
		`Example: [TOOL_CALLS][{"name":"read","arguments":{"path":"x"}}]`,
	} {
		for _, protocol := range []modelcall.ToolCallProtocol{modelcall.ToolProtocolNative, modelcall.ToolProtocolEnvelope} {
			req := toolReq()
			req.ToolProtocol = protocol
			out, err := ClassifyToolResponse(providerprofile.OpenAI(), req, &modelcall.Completion{Content: content}, "p", "m")
			if err != nil || out == nil || len(out.Completion.ToolCalls) > 0 || len(out.TextActionTools) > 0 || out.Completion.Content != content {
				t.Fatalf("example changed: out=%+v err=%v", out, err)
			}
		}
	}
	req := toolReq()
	req.ToolProtocol = modelcall.ToolProtocolNative
	out, err := ClassifyToolResponse(providerprofile.OpenAI(), req, &modelcall.Completion{Content: `{"name":"read","arguments":{"path":"x"}}`}, "p", "m")
	if err != nil || len(out.Completion.ToolCalls) > 0 || len(out.TextActionTools) != 1 || out.TextActionTools[0] != "read" {
		t.Fatalf("plain text must request repair: %+v %v", out, err)
	}
}
