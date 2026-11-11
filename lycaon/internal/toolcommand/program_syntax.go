package toolcommand

import (
	"errors"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
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
		unit := diagnostic.SourceName
		if unit == "" {
			unit = "submitted"
		}
		facts, projectionErr := (toolcontract.ProgramDiagnostic{
			Stage: toolcontract.SyntaxDiagnosticStage, Code: diagnostic.Code,
			SourceUnitID: unit, Span: diagnostic.Span, Expected: diagnostic.Expected,
			Limit: diagnostic.Limit, Unit: diagnostic.Unit, Maximum: diagnostic.Maximum,
			Observed: diagnostic.Observed, Parser: diagnostic.Parser,
		}).Feedback()
		if projectionErr != nil {
			return nil //nolint:nilerr // A diagnostic without a projection adds no feedback to the original refusal.
		}
		if diagnostic.Sourced && diagnostic.SourceName == "" {
			delete(facts, "source_unit_id")
			facts["source_unit_id_omitted"] = true
		}
		facts["tool"] = tool
		facts["reason"] = diagnostic.Expected
		facts["diagnostic_top_level"] = !diagnostic.Sourced
		if !diagnostic.Sourced {
			facts["command_execution"] = "not_started"
		}
		if diagnostic.Limit != "" {
			facts["limit_id"], facts["resource_unit"] = diagnostic.Limit, diagnostic.Unit
			facts["maximum"], facts["observed"] = diagnostic.Maximum, diagnostic.Observed
		}
		return toolrejection.RejectInvalidArguments(diagnostic.Code, facts)
	default:
		return nil
	}
}
