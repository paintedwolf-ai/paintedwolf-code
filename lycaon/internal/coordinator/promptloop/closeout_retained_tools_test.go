package promptloop

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRetainedAnswerSurvivesObservingToolsOnly(t *testing.T) {
	const assistant = "assistant-1"
	read := `{"state":"completed","status":0,"stdout":"package x","completed_effects":0,"uncertain_effects":0}`
	wrote := `{"state":"completed","status":0,"completed_effects":1,"uncertain_effects":0}`
	maybe := `{"state":"completed","status":0,"completed_effects":0,"uncertain_effects":1}`
	running := `{"state":"running","completed_effects":0,"uncertain_effects":0}`
	for _, tc := range []struct {
		name    string
		calls   []string
		results map[string]string
		keeps   bool
	}{
		{"read-only tool", []string{"read"}, nil, true},
		{"recall", []string{"recall"}, nil, true},
		{"bookkeeping", []string{"update_progress"}, nil, true},
		{"read-only command", []string{"command"}, map[string]string{"command": read}, true},
		{"command with effects", []string{"command"}, map[string]string{"command": wrote}, false},
		{"command with uncertain effects", []string{"command"}, map[string]string{"command": maybe}, false},
		{"command still running", []string{"command"}, map[string]string{"command": running}, false},
		{"command with unreadable outcome", []string{"command"}, map[string]string{"command": "refused"}, false},
		{"command with no outcome", []string{"command"}, nil, false},
		{"write", []string{"write"}, nil, false},
		{"observer beside a write", []string{"recall", "write"}, nil, false},
		{"unknown tool", []string{"no_such_tool"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := tools.NewStubRegistry()
			for _, name := range []string{"recall", "update_progress", "command"} {
				_ = reg.Register(name, func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "", nil })
			}
			retained := guidance.RetainedCloseout{Active: true}
			loop := NewPromptLoopForTest(PromptLoopDeps{
				Context:  ContextDeps{Tools: reg},
				Closeout: CloseoutDeps{ResumeCloseoutWork: func(context.Context, string) { retained.Active = false }},
			})
			var calls []api.ToolCall
			var history []api.Message
			for _, name := range tc.calls {
				calls = append(calls, api.ToolCall{ID: name + "-1", Name: name})
				if output, ok := tc.results[name]; ok {
					history = append(history, api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: name, ToolCallID: name + "-1", AssistantMessageID: assistant, Content: output}})
				}
			}
			settle := loop.Closeout.keepRetainedForObservers(t.Context(), "s", &promptLoopTurnState{}, calls)
			settle(history, assistant)
			if retained.Active != tc.keeps {
				t.Fatalf("retained after batch = %t, want %t", retained.Active, tc.keeps)
			}
		})
	}
}
