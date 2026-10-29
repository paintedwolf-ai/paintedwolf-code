package toolcontract

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
)

// ProgramDiagnosticStage names where language admission or evaluation refused.
// Only the syntax stage precedes every program effect; later stages refuse a
// reached command whose earlier effects the receipt ledger reports separately.
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
		facts["supported_options"] = commandSpellings(command)
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
	return facts, nil
}

func commandSpellings(command CommandSpecification) []string {
	out := []string{}
	for _, form := range command.Forms {
		out = append(out, form.Spellings...)
	}
	sort.Strings(out)
	return out
}

// ProgramRepairRequest supplies the exact submitted unit and current facts a
// replacement must satisfy. Validate parses and binds the complete proposed
// unit against the active catalog; Offered reports that the current surface
// lets the model submit command. A repair never executes or grants authority.
type ProgramRepairRequest struct {
	Source   []byte
	Offered  bool
	Validate func([]byte) error
}

// ProgramRepair is one exact, validated byte replacement for explicit
// submission. Program is the complete corrected syntax unit.
type ProgramRepair struct {
	SourceUnitID string
	Span         SourceSpan
	Text         string
	Program      []byte
}

// Feedback publishes the exact replacement coordinates, never the program.
func (r ProgramRepair) Feedback() map[string]any {
	return map[string]any{
		"replacement_source_unit_id": r.SourceUnitID, "replacement_start_byte": r.Span.StartByte,
		"replacement_end_byte": r.Span.EndByte, "replacement_text": r.Text, "replacement_submission": "explicit",
	}
}

// Repair returns the single mechanically proven correction or nil. The only
// proven form is an unquoted long option that abbreviates exactly one of its
// command's catalog spellings; ambiguity, quoting, expansion, an unavailable
// surface or a failed validation leaves the precise diagnosis without one.
func (d ProgramDiagnostic) Repair(req ProgramRepairRequest) (*ProgramRepair, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	if d.Stage != OptionDiagnosticStage || d.Code != optionUnsupportedCode || !req.Offered || req.Validate == nil {
		return nil, nil
	}
	command, _, err := diagnosticCommand(d.Command)
	if err != nil {
		return nil, err
	}
	if d.Span.EndByte > len(req.Source) || !bytes.Equal(req.Source[d.Span.StartByte:d.Span.EndByte], []byte(d.Word)) || !literalOptionWord(d.Word) {
		return nil, nil
	}
	name, value, assigned := strings.Cut(d.Word, "=")
	var match *OptionForm
	spelling := ""
	for i := range command.Forms {
		for _, candidate := range command.Forms[i].Spellings {
			if !strings.HasPrefix(candidate, "--") || !strings.HasPrefix(candidate, name) || candidate == name {
				continue
			}
			if match != nil {
				return nil, nil
			}
			match, spelling = &command.Forms[i], candidate
		}
	}
	if match == nil || (assigned && match.Arity == 0) {
		return nil, nil
	}
	text := spelling
	if assigned {
		text += "=" + value
	}
	program := make([]byte, 0, len(req.Source)-len(d.Word)+len(text))
	program = append(append(append(program, req.Source[:d.Span.StartByte]...), text...), req.Source[d.Span.EndByte:]...)
	if req.Validate(program) != nil {
		return nil, nil
	}
	return &ProgramRepair{SourceUnitID: d.SourceUnitID, Span: d.Span, Text: text, Program: program}, nil
}

// literalOptionWord admits only bytes the shell reads as themselves, so the
// span is the whole word and no quote, escape or expansion is reinterpreted.
func literalOptionWord(word string) bool {
	if len(word) < 3 || !strings.HasPrefix(word, "--") {
		return false
	}
	for i := 0; i < len(word); i++ {
		c := word[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-_=.,:/+", c) >= 0) {
			return false
		}
	}
	return true
}
