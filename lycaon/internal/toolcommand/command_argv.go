package toolcommand

import (
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strconv"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/commandsurface"
)

// Command plan rejection codes.
const (
	CommandRedirectionUnsupportedCode = "COMMAND_REDIRECTION_UNSUPPORTED"
	CommandGlobBudgetCode             = "COMMAND_GLOB_BUDGET_EXCEEDED"
)

// ArgvShapeObservation maps command/verify argv-shape errors to agent-public codes.
func ArgvShapeObservation(toolName string, err error) *toolrejection.ToolReject {
	if err == nil {
		return nil
	}
	var redirection *argv.RedirectionError
	var budget *commandsurface.GlobBudgetError
	switch {
	case errors.Is(err, commandsurface.ErrArgvRequired):
		switch toolName {
		case "command":
			return toolrejection.RejectInvalidArguments("COMMAND_ARGV_REQUIRED", map[string]any{"tool": toolName})
		case "verify":
			return &toolrejection.ToolReject{Code: "VERIFY_COMMAND_UNDECLARED", Data: map[string]any{"tool": toolName}}
		}
	case errors.Is(err, commandsurface.ErrArgvConflict):
		switch toolName {
		case "command", "verify":
			return toolrejection.RejectInvalidArguments("COMMAND_ARGV_CONFLICT", map[string]any{"tool": toolName})
		}
	case errors.Is(err, commandsurface.ErrPipelineShape),
		errors.Is(err, commandsurface.ErrStdinSources),
		errors.Is(err, commandsurface.ErrAppendWithoutTarget):
		switch toolName {
		case "command", "verify":
			return toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": err.Error(), "tool": toolName})
		}
	case errors.As(err, &redirection):
		switch toolName {
		case "command", "verify", "terminal_open":
			return toolrejection.RejectInvalidArguments(CommandRedirectionUnsupportedCode, map[string]any{
				"tool": toolName, "redirection_issue": string(redirection.Issue), "operator": redirection.Operator,
				"redirection_conflict": redirection.Issue == argv.IssueStdoutConflict ||
					redirection.Issue == argv.IssueStderrConflict || redirection.Issue == argv.IssueStdinConflict,
				"redirection_pipe":     redirection.Issue == argv.IssuePipeStdout || redirection.Issue == argv.IssuePipeStdin,
				"redirection_terminal": redirection.Issue == argv.IssueSurfaceUnsupported,
			})
		}
	case errors.As(err, &budget):
		switch toolName {
		case "command", "verify":
			return toolrejection.RejectInvalidArguments(CommandGlobBudgetCode, map[string]any{
				"tool": toolName, "pattern": budget.Pattern, "glob_limit": budget.Limit, "max": strconv.Itoa(budget.Max),
			})
		}
	}
	return nil
}

func ArgvShapeReject(tool string, args map[string]any) *toolrejection.ToolReject {
	switch tool {
	case "command", "verify":
		_, err := commandsurface.ParsePlan(args)
		return ArgvShapeObservation(tool, err)
	default:
		return nil
	}
}
