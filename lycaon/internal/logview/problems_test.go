package logview

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCollectProblems(t *testing.T) {
	sessions := []SessionRecord{
		{SessionID: "c", AgentType: "coordinator", Task: "do"},
		{SessionID: "w", ParentSessionID: "c", AgentType: "command-verifier", Task: "scope"},
	}
	llm := []LLMRecord{
		{SessionID: "c", AgentType: "coordinator", Iteration: 1, Messages: []LLMMessage{
			{Role: "user", Content: rawStr(t, "Rejected: nope\nCode: WRITE_SCOPE_DENIED")},
		}},
		{SessionID: "w", AgentType: "command-verifier", Iteration: 1, Messages: []LLMMessage{
			{Role: "user", Content: rawStr(t, "start")},
			{Role: "assistant", Content: rawStr(t, "run"), ToolCalls: []LLMToolCall{
				{Name: "command", Args: json.RawMessage(`{"cmd":"x"}`)},
			}},
			{Role: "tool", Content: rawStr(t, `{"ExitCode":1,"out":"boom"}`)},
		}},
		{SessionID: "w", AgentType: "command-verifier", Iteration: 2, Messages: []LLMMessage{
			{Role: "user", Content: rawStr(t, "[host:worker-closeout]\nfinal")},
		}},
	}
	httpRecs := []HTTPRecord{{Method: "POST", Path: "/x", Status: 500}}
	denPerf := []DenPerfRecord{{
		Event:  "loop-stall",
		Detail: map[string]any{"lag_ms": float64(748), "recent": "thumbnail.capture:start"},
	}}

	got := CollectProblems(BuildSessionTree(sessions, llm), httpRecs, denPerf)
	kinds := map[ProblemKind]int{}
	for _, p := range got {
		kinds[p.Kind]++
	}
	for _, k := range []ProblemKind{ProblemWorker, ProblemRejection, ProblemToolError, ProblemHTTP, ProblemStall} {
		if kinds[k] == 0 {
			t.Errorf("expected a problem of kind %d, got none (all: %+v)", k, got)
		}
	}
}

func TestToolExitFailure(t *testing.T) {
	if _, bad := toolExitFailure(`{"ExitCode":0}`); bad {
		t.Error("exit 0 is not a failure")
	}
	if code, bad := toolExitFailure(`{"ExitCode":2,"x":1}`); !bad || code != "2" {
		t.Errorf("exit 2 → code=%q bad=%v", code, bad)
	}
	if _, bad := toolExitFailure(`{"available":false}`); bad {
		t.Error("git unavailable is benign, not a failure")
	}
}

func TestRenderProblemsEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := testDisplay().RenderProblems(&buf, nil); err != nil {
		testutil.FailErr(t, "testDisplay failed", err)
	}
	if !strings.Contains(buf.String(), "no problems") {
		t.Errorf("empty render = %q", buf.String())
	}
}
