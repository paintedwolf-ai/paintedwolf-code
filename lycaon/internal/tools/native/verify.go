package native

import (
	"context"
	"maps"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

// VerifyTool runs the selected project check through the command program runtime.
type VerifyTool struct {
	Programs        *command.ProgramRuntime
	DeclaredCommand command.DeclaredVerifyCommand
}

func (t *VerifyTool) Run(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
	declared := ""
	if t.DeclaredCommand != nil {
		declared = strings.TrimSpace(t.DeclaredCommand(tc.ActiveRootPath()))
	}
	requested := tc.Effects.RequestedArgs
	if requested == nil {
		requested = command.CanonicalToolArgs(tc, args)
	}
	if err := rejectDeclaredCommandOverride(declared, requested); err != nil {
		return "", err
	}
	tc.Effects.RequestedArgs = requested
	program := maps.Clone(args)
	if program == nil {
		program = map[string]any{}
	}
	if source, _ := program["command"].(string); strings.TrimSpace(source) == "" {
		if declared == "" {
			return "", &toolrejection.ToolReject{Code: "VERIFY_COMMAND_UNDECLARED"}
		}
		program["command"] = declared
	}
	tc.Execution.VerificationCheck = true
	return t.Programs.Run(ctx, "verify", program, tc)
}

func rejectDeclaredCommandOverride(declared string, args map[string]any) error {
	if declared == "" {
		return nil
	}
	requested := commandsurface.PrimaryCommandLine(args, nil)
	if requested == "" || ((strings.TrimSpace(requested) == strings.TrimSpace(declared) || commandsurface.SameCommandLine(requested, declared)) && args["pipeline"] == nil) {
		return nil
	}
	return &toolrejection.ToolReject{Code: "VERIFY_DECLARED_COMMAND_OVERRIDE", Data: map[string]any{
		"declared": declared, "requested": requested,
	}}
}
