package toolcontract

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func optionDiagnostic(source, word string) ProgramDiagnostic {
	start := strings.Index(source, word)
	return ProgramDiagnostic{Stage: OptionDiagnosticStage, Code: optionUnsupportedCode, Command: "grep", Word: word, SourceUnitID: "submitted", Span: SourceSpan{StartByte: start, EndByte: start + len(word), Line: 1, Column: start + 1}}
}

func TestProgramDiagnosticFeedbackCarriesSpanAndCatalogHelp(t *testing.T) {
	d := optionDiagnostic("grep --sparkle x", "--sparkle")
	facts, err := d.Feedback()
	if err != nil {
		testutil.FailErr(t, "read structured program diagnostic", err)
	}
	options, _ := facts["supported_options"].([]string)
	if facts["diagnostic_stage"] != "option" || facts["command"] != "grep" || facts["diagnostic_word"] != "--sparkle" || facts["source_start_byte"] != 5 || facts["source_end_byte"] != 14 || facts["diagnostic_pre_evaluation"] != false {
		t.Fatalf("option feedback = %+v", facts)
	}
	if !strings.HasPrefix(facts["expected_form"].(string), "grep [OPTIONS]") || !slices.Contains(options, "--recursive") || slices.Contains(options, "--sparkle") {
		t.Fatalf("catalog help = %v %v", facts["expected_form"], options)
	}
	syntax := ProgramDiagnostic{Stage: SyntaxDiagnosticStage, Code: "COMMAND_SYNTAX_INVALID", Expected: "closing quote", SourceUnitID: "submitted", Span: SourceSpan{StartByte: 4, EndByte: 9, Line: 1, Column: 5}, Limit: "source_bytes", Unit: "bytes", Maximum: 8, Observed: 9}
	facts, err = syntax.Feedback()
	if err != nil || facts["diagnostic_pre_evaluation"] != true || facts["expected_form"] != "closing quote" || facts["limit_observed"] != int64(9) || facts["command"] != nil {
		t.Fatalf("syntax feedback = %+v %v", facts, err)
	}
	for name, invalid := range map[string]ProgramDiagnostic{
		"stage":   {Stage: "guess", Code: "X", SourceUnitID: "u", Span: SourceSpan{Line: 1, Column: 1}},
		"code":    {Stage: SyntaxDiagnosticStage, Code: "retry the program", SourceUnitID: "u", Span: SourceSpan{Line: 1, Column: 1}},
		"span":    {Stage: SyntaxDiagnosticStage, Code: "X", SourceUnitID: "u", Span: SourceSpan{StartByte: 3, EndByte: 2, Line: 1, Column: 1}},
		"command": {Stage: OptionDiagnosticStage, Code: optionUnsupportedCode, SourceUnitID: "u", Span: SourceSpan{Line: 1, Column: 1}},
		"unknown": {Stage: OptionDiagnosticStage, Code: optionUnsupportedCode, Command: "not-a-command", SourceUnitID: "u", Span: SourceSpan{Line: 1, Column: 1}},
	} {
		if _, err := invalid.Feedback(); err == nil {
			t.Errorf("%s: invalid diagnostic projected", name)
		}

	}
}
