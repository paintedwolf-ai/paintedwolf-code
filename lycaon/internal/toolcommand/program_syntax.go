package toolcommand

import (
	"errors"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// ProgramSyntaxObservation projects only typed parser facts through the normal
// rejection contract. It never fabricates source corrections or replacement calls.
func ProgramSyntaxObservation(tool string, err error) *toolrejection.ToolReject {
	if tool != "command" && tool != "verify" {
		return nil
	}
	var diagnostic *commandsurface.SyntaxDiagnostic
	if !errors.As(err, &diagnostic) {
		return nil
	}
	switch diagnostic.Code {
	case "COMMAND_SYNTAX_INVALID", "COMMAND_SYNTAX_UNSUPPORTED", "COMMAND_LIMIT_EXCEEDED", "COMMAND_OPERAND_INVALID":
		return toolrejection.RejectInvalidArguments(diagnostic.Code, map[string]any{
			"tool": tool, "source_start_byte": diagnostic.Span.StartByte, "source_end_byte": diagnostic.Span.EndByte,
			"source_line": diagnostic.Span.Line, "source_column": diagnostic.Span.Column, "expected_form": diagnostic.Expected, "reason": diagnostic.Expected,
			"limit_id": diagnostic.Limit, "resource_unit": diagnostic.Unit, "maximum": diagnostic.Maximum, "observed": diagnostic.Observed,
		})
	default:
		return nil
	}
}
