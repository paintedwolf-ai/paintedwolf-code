package commandsurface

import (
	"errors"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"mvdan.cc/sh/v3/syntax"
)

func parserDiagnostic(err error, length int) error {
	var budget *syntax.BudgetError
	if errors.As(err, &budget) {
		if budget.Cause != nil {
			return budget.Cause
		}
		return &SyntaxDiagnostic{Code: "COMMAND_LIMIT_EXCEEDED", Span: parserPosition(budget.Pos, length), Expected: "bounded parser working storage", Limit: "evaluation_memory_bytes", Unit: "bytes", Maximum: budget.Maximum, Observed: budget.Observed}
	}
	var dialect syntax.LangError
	if errors.As(err, &dialect) {
		return &SyntaxDiagnostic{Code: "COMMAND_SYNTAX_UNSUPPORTED", Span: parserPosition(dialect.Pos, length), Expected: "Bash syntax", Parser: &toolcontract.ParserDiagnostic{Kind: "unsupported_dialect", Message: dialect.Feature}}
	}
	span := unitStart()
	var detail *toolcontract.ParserDiagnostic
	var parse syntax.ParseError
	if errors.As(err, &parse) {
		span = parserPosition(parse.Pos, length)
		detail = &toolcontract.ParserDiagnostic{
			Kind: string(parse.Diagnostic.Kind), Message: parse.Text,
			Expected: parse.Diagnostic.Expected, Found: parse.Diagnostic.Found,
			Detected: parserPosition(parse.Diagnostic.At, length), Incomplete: parse.Incomplete,
		}
	}
	return &SyntaxDiagnostic{Code: "COMMAND_SYNTAX_INVALID", Span: span, Expected: "complete Bash source", Parser: detail}
}
