package native

import (
	"context"

	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

// FormTool is a callable tool that runs through its native command form, so both
// routes share one operation, result, receipt and review. The host writes the
// program from typed arguments, which keeps them out of the model's shell quoting.
type FormTool struct {
	Name     string
	Programs *command.ProgramRuntime
	Argv     func(map[string]any) []string
}

// Run executes the form the arguments select.
func (t *FormTool) Run(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
	if tc.Effects.RequestedArgs == nil {
		tc.Effects.RequestedArgs = command.CanonicalToolArgs(tc, args)
	}
	return t.Programs.Run(ctx, t.Name, map[string]any{"command": toolreach.QuoteProgram(t.Argv(args))}, tc)
}
