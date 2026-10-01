package openaicompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/providerwire"
)

func accWithRawArgs(name, raw string) map[int]*providerwire.StreamTool {
	slot := &providerwire.StreamTool{ID: "call_1", Name: name}
	slot.Args.WriteString(raw)
	return map[int]*providerwire.StreamTool{0: slot}
}

func TestToolCallsTruncatedAtLengthCap(t *testing.T) {
	// A whole-file write cut mid-JSON by the completion cap: unterminated args.
	raw := `{"path":"index.html","content":"` + strings.Repeat("x", 200)
	calls := providerwire.CollectToolCalls(accWithRawArgs("write", raw), true, true)
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	if !calls[0].ArgsTruncated {
		t.Fatal("ArgsTruncated = false for length-capped unterminated args")
	}
	if calls[0].ArgsMalformed {
		t.Fatal("ArgsMalformed = true for length-capped args (should be truncated)")
	}
	if calls[0].Args != nil {
		t.Fatalf("Args = %+v want nil", calls[0].Args)
	}
}

func TestToolCallsCompleteArgsNotTruncated(t *testing.T) {
	calls := providerwire.CollectToolCalls(accWithRawArgs("write", `{"path":"a.txt","content":"hi"}`), true, true)
	if len(calls) != 1 || calls[0].ArgsTruncated || calls[0].ArgsMalformed {
		t.Fatalf("complete args wrongly flagged: %+v", calls)
	}
	if calls[0].Args["path"] != "a.txt" {
		t.Fatalf("Args = %+v", calls[0].Args)
	}
}

func TestToolCallsMalformedArgsOnCompletedStream(t *testing.T) {
	// Model emitted broken JSON and the stream finished normally (not the token
	// cap) — a serialization problem (TOOL_ARGS_MALFORMED), not a size problem.
	calls := providerwire.CollectToolCalls(accWithRawArgs("summarize", `{"task":"x" } ]`), false, true)
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[0].ArgsTruncated {
		t.Fatal("completed malformed args wrongly marked truncated")
	}
	if !calls[0].ArgsMalformed {
		t.Fatal("ArgsMalformed = false for completed broken JSON")
	}
	if calls[0].Args != nil {
		t.Fatalf("Args = %+v want nil", calls[0].Args)
	}
}

func TestToolCallsProgressSnapshotsNeverFlagged(t *testing.T) {
	// Mid-stream partial args never parse; progress snapshots (done=false) must
	// not flag as truncated or malformed.
	calls := providerwire.CollectToolCalls(accWithRawArgs("write", `{"path":"index.html","cont`), false, false)
	if len(calls) != 1 || calls[0].ArgsTruncated || calls[0].ArgsMalformed {
		t.Fatalf("progress snapshot wrongly flagged: %+v", calls)
	}
}

func TestToolCallFromWireMarksMalformedOnNormalFinish(t *testing.T) {
	// Non-stream path: broken JSON with a normal finish is malformed, not truncated.
	tc := toolCallWire{ID: "call_1"}
	tc.Function.Name = "summarize"
	tc.Function.Arguments = `{"task":"x" } ]`
	call := toolCallFromWire(tc, false)
	if call.ArgsTruncated {
		t.Fatal("non-stream malformed args wrongly marked truncated")
	}
	if !call.ArgsMalformed {
		t.Fatal("non-stream broken JSON not marked malformed")
	}
}

func TestMapChatCompletionMarksLengthCappedArgs(t *testing.T) {
	raw := `{
		"choices": [{
			"finish_reason": "length",
			"message": {
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {"name": "write", "arguments": "{\"path\":\"index.html\",\"content\":\"trunca"}
				}]
			}
		}]
	}`
	var resp chatCompletionResponseWire
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out := mapChatCompletionWire(&resp)
	if out == nil || len(out.ToolCalls) != 1 {
		t.Fatalf("completion = %+v", out)
	}
	if !out.ToolCalls[0].ArgsTruncated {
		t.Fatal("non-stream length-capped args not marked truncated")
	}
}
