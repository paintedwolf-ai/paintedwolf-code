package toolcontract

import (
	"fmt"
	"sort"
	"strings"
)

// ProgramDiagnosticStage names where language admission or evaluation refused.
// A syntax refusal precedes evaluation of its source unit. The ledger reports
// effects of enclosing units separately.
type ProgramDiagnosticStage string

const (
	SyntaxDiagnosticStage     ProgramDiagnosticStage = "syntax"
	OptionDiagnosticStage     ProgramDiagnosticStage = "option"
	ExpansionDiagnosticStage  ProgramDiagnosticStage = "expansion"
	EvaluationDiagnosticStage ProgramDiagnosticStage = "evaluation"
)

const optionUnsupportedCode = "COMMAND_OPTION_UNSUPPORTED"

// ProgramDiagnostic is the typed form of a parser, argv binder, expansion or
// evaluator refusal (commandsurface.SyntaxDiagnostic, commandprogram.ArgvError,
// ExpansionError and EvaluationRefusal map into it). Code keeps its public
// meaning; Expected is the catalog's declared form, never prose to parse.
type ProgramDiagnostic struct {
	Stage             ProgramDiagnosticStage
	Code              string
	Command           string
	Word              string
	Expected          string
	SourceUnitID      string
	Span              SourceSpan
	Limit, Unit       string
	Maximum, Observed int64
	Parser            *ParserDiagnostic
}

func (d ProgramDiagnostic) validate() error {
	switch d.Stage {
	case SyntaxDiagnosticStage, OptionDiagnosticStage, ExpansionDiagnosticStage, EvaluationDiagnosticStage:
	default:
		return fmt.Errorf("unknown program diagnostic stage %q", d.Stage)
	}
	if d.Code == "" || strings.Trim(d.Code, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_") != "" {
		return fmt.Errorf("program diagnostic requires a public rejection code")
	}
	if d.SourceUnitID == "" || d.Span.StartByte < 0 || d.Span.EndByte < d.Span.StartByte || d.Span.Line < 1 || d.Span.Column < 1 {
		return fmt.Errorf("program diagnostic requires a source unit and span")
	}
	if d.Stage == OptionDiagnosticStage && d.Command == "" {
		return fmt.Errorf("option diagnostic requires its catalog command")
	}
	return nil
}

func diagnosticCommand(name string) (CommandSpecification, bool, error) {
	if name == "" {
		return CommandSpecification{}, false, nil
	}
	catalog, err := operationLanguage()
	if err != nil {
		return CommandSpecification{}, false, err
	}
	for _, command := range catalog.Commands {
		if command.Name == name {
			return command, true, nil
		}
	}
	return CommandSpecification{}, false, fmt.Errorf("program diagnostic names unknown catalog command %q", name)
}

// Feedback projects the diagnostic and the frozen catalog's help for its
// command into rejection details. It never states an effect outcome; the
// receipt ledger supplies those facts.
func (d ProgramDiagnostic) Feedback() (map[string]any, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	command, found, err := diagnosticCommand(d.Command)
	if err != nil {
		return nil, err
	}
	facts := map[string]any{
		"diagnostic_stage": string(d.Stage), "source_unit_id": d.SourceUnitID,
		"source_start_byte": d.Span.StartByte, "source_end_byte": d.Span.EndByte,
		"source_line": d.Span.Line, "source_column": d.Span.Column,
		"diagnostic_pre_evaluation": d.Stage == SyntaxDiagnosticStage,
	}
	expected := d.Expected
	if found {
		facts["command"] = command.Name
		if expected == "" {
			expected = command.Argv
		}
		facts["supported_options"] = CommandSpellings(command)
	}
	if expected != "" {
		facts["expected_form"] = expected
	}
	if d.Word != "" {
		facts["diagnostic_word"] = d.Word
	}
	if d.Limit != "" {
		facts["limit"], facts["limit_unit"], facts["limit_maximum"], facts["limit_observed"] = d.Limit, d.Unit, d.Maximum, d.Observed
	}
	if d.Parser != nil {
		d.Parser.addFacts(facts)
	}
	return facts, nil
}

// CommandSpellings lists every option spelling a catalog command accepts.
func CommandSpellings(command CommandSpecification) []string {
	out := []string{}
	for _, form := range command.Forms {
		out = append(out, form.Spellings...)
	}
	sort.Strings(out)
	return out
}
