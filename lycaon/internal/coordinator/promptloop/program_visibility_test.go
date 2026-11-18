package promptloop

import (
	"testing"

	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProgramRefusalVisibilitySharesDirectClassificationBeforeAdmission(t *testing.T) {
	hints := loadCoordinatorTestHintConfig(t)
	loop := NewPromptLoop(PromptLoopDeps{Closeout: CloseoutDeps{HintConfig: hints}})
	cases := []struct {
		code string
		want api.ToolResultUiVisibility
	}{
		{"COMMAND_SYNTAX_INVALID", api.ToolResultUiVisibilityBenign},
		{"COMMAND_OPERAND_INVALID", api.ToolResultUiVisibilityBenign},
		{"COMMAND_NATIVE_FORM_UNSUPPORTED", api.ToolResultUiVisibilityBenign},
		{"PROGRESS_MISSING", api.ToolResultUiVisibilityBenign},
		{"SANDBOX_CAPABILITY_REQUEST_INVALID", api.ToolResultUiVisibilityBenign},
		{"SURVEY_PATH_ESCAPE", api.ToolResultUiVisibilityBenign},
		{"TOOL_ARGS_INVALID", api.ToolResultUiVisibilityBenign},
		{approvaloutcome.CodeApprovalDenied, api.ToolResultUiVisibilityNormal},
		{approvaloutcome.CodeApprovalExpired, api.ToolResultUiVisibilityNormal},
		{approvaloutcome.CodeApprovalCanceled, api.ToolResultUiVisibilityNormal},
		{"APPROVAL_RULE_DENIED", api.ToolResultUiVisibilityNormal},
	}
	for _, name := range []string{"command", "verify", "summarize", "survey_repo", "code_rewrite"} {
		t.Run(name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.code, func(t *testing.T) {
					const content = `{"state":"rejected","completed_effects":0,"uncertain_effects":0}`
					call := api.ToolCall{ID: "call", Name: name}
					direct := guidance.ComposeToolResult(content, guidance.ToolResultFacts{}.WithCode(tc.code), hints)
					if direct.UiVisibility != tc.want {
						t.Fatalf("direct visibility = %s, want %s", direct.UiVisibility, tc.want)
					}
					out := &tools.ToolInvocationOut{ProgramRefusal: &tools.ProgramRefusalVisibility{Code: tc.code, BeforeAdmission: true}}
					assertVisibility := func(want api.ToolResultUiVisibility) {
						t.Helper()
						run := toolInvocation{content: content, captures: toolCapturesFrom(out)}
						msg := loop.Tools.composeToolResultMessage(t.Context(), nil, "session", call, "assistant", api.MessageOriginTool, &run)
						if msg.ToolResult.UiVisibility != want || msg.Content != content || len(msg.ToolResult.Codes) != 0 {
							t.Fatalf("tool result = %+v, want visibility %s", msg.ToolResult, want)
						}
					}
					assertVisibility(tc.want)
					out.ProgramRefusal.BeforeAdmission = false
					assertVisibility(api.ToolResultUiVisibilityNormal)
					out.ProgramRefusal.BeforeAdmission = true
					out.MarkProgramProgressPublished()
					assertVisibility(api.ToolResultUiVisibilityNormal)
				})
			}
		})
	}
}
