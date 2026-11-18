package native

import (
	"context"

	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

// FormTool is a callable tool that runs through its native command form, so both
// routes share one operation, result, receipt and review. The host writes the
// program from typed arguments, which keeps them out of the model's shell quoting.
type FormTool struct {
	Programs *command.ProgramRuntime
	Argv     func(map[string]any) []string
}

// Run executes the form the arguments select.
func (t *FormTool) Run(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
	argv := t.Argv(args)
	if len(argv) == 0 {
		return "", toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"tool": tc.Invocation.ToolName})
	}
	if tc.Effects.RequestedArgs == nil {
		tc.Effects.RequestedArgs = command.CanonicalToolArgs(tc, args)
	}
	return t.Programs.Run(ctx, "command", map[string]any{"command": toolreach.QuoteProgram(argv)}, tc)
}
