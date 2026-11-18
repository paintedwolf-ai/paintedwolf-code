package actioncard

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

// Every call on one terminal shares the session's fold key, whether the
// handle came back from terminal_open or is named by a later call.
func TestTerminalCallsShareTheirSession(t *testing.T) {
	open := ForResult(api.ToolResult{Tool: "terminal_open", ToolArgs: map[string]any{"command": "python3 game.py"}, Content: "{}",
		Process: &api.ToolProcessHandle{Handle: "term-7", Running: true}}, nil)
	send := ForResult(api.ToolResult{Tool: "terminal_send", ToolArgs: map[string]any{"id": "term-7", "input": "1\n"}, DisplaySubject: "term-7", Content: "{}"}, nil)
	other := ForResult(api.ToolResult{Tool: "terminal_read", ToolArgs: map[string]any{"id": "term-8"}, DisplaySubject: "term-8", Content: "{}"}, nil)
	closed := ForResult(api.ToolResult{Tool: "terminal_close", ToolArgs: map[string]any{"id": "term-7"}, DisplaySubject: "term-7", Content: "{}"}, nil)
	if open.FoldKey != "terminal:term-7" || send.FoldKey != open.FoldKey || closed.FoldKey != open.FoldKey || other.FoldKey == open.FoldKey {
		t.Fatalf("fold keys %q %q %q %q", open.FoldKey, send.FoldKey, closed.FoldKey, other.FoldKey)
	}
	if open.Label != "Terminal session" || open.Ran.Text != "python3 game.py" {
		t.Fatalf("open card %+v", open)
	}
	for name, d := range map[string]*api.ActionDescriptor{"open": open, "send": send, "close": closed} {
		steps, _ := fact(d.Facts, api.ActionFactSteps)
		if len(steps.Sequence) != 1 || steps.Unit != "actions" {
			t.Fatalf("%s steps %+v", name, steps)
		}
		if d.Opener == nil || d.Opener.Kind != "terminal" || d.Opener.ProcessHandle != "term-7" {
			t.Fatalf("%s opener %+v", name, d.Opener)
		}
	}
	if result, _ := fact(closed.Facts, api.ActionFactResult); result.Outcome != "completed" {
		t.Fatalf("closed session result %+v", result)
	}
}
