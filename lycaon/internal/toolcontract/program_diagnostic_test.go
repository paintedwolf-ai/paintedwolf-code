package toolcontract

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func optionDiagnostic(source, word string) ProgramDiagnostic {
	start := strings.Index(source, word)
	return ProgramDiagnostic{Stage: OptionDiagnosticStage, Code: optionUnsupportedCode, Command: "grep", Word: word, SourceUnitID: "submitted", Span: SourceSpan{StartByte: start, EndByte: start + len(word), Line: 1, Column: start + 1}}
}

func TestProgramDiagnosticFeedbackCarriesSpanAndCatalogHelp(t *testing.T) {
	d := optionDiagnostic("grep --colour x", "--colour")
	facts, err := d.Feedback()
	if err != nil {
		t.Fatal(err)
	}
	options, _ := facts["supported_options"].([]string)
	if facts["diagnostic_stage"] != "option" || facts["command"] != "grep" || facts["diagnostic_word"] != "--colour" || facts["source_start_byte"] != 5 || facts["source_end_byte"] != 13 || facts["diagnostic_pre_evaluation"] != false {
		t.Fatalf("option feedback = %+v", facts)
	}
	if !strings.HasPrefix(facts["expected_form"].(string), "grep [OPTIONS]") || !slices.Contains(options, "--recursive") || slices.Contains(options, "--colour") {
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
		if _, err := invalid.Repair(ProgramRepairRequest{Offered: true, Validate: func([]byte) error { return nil }}); err == nil {
			t.Errorf("%s: invalid diagnostic repaired", name)
		}
	}
}

func TestProgramRepairOffersOnlyProvenUniqueOptionCorrections(t *testing.T) {
	accept := func([]byte) error { return nil }
	for _, tc := range []struct {
		name, source, word, text string
		validate               func([]byte) error
		offered                bool
	}{
		{"unique abbreviation", "grep --recur x .", "--recur", "--recursive", accept, true},
		{"value keeps its bytes", "grep --max=3 x", "--max=3", "--max-count=3", accept, true},
		{"ambiguous prefix", "grep --excl=a x", "--excl=a", "", accept, true},
		{"several spellings", "grep --file x", "--file", "", accept, true},
		{"boolean given a value", "grep --recur=1 x", "--recur=1", "", accept, true},
		{"quoted word", `grep "--recur" x`, `"--recur"`, "", accept, true},
		{"expansion bytes", "grep --rec$X x", "--rec$X", "", accept, true},
		{"short option", "grep -Z x", "-Z", "", accept, true},
		{"command not offered", "grep --recur x", "--recur", "", accept, false},
		{"validation fails", "grep --recur x", "--recur", "", func([]byte) error { return errors.New("binder refused") }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := optionDiagnostic(tc.source, tc.word)
			if tc.name == "quoted word" {
				d.Word = "--recur"
			}
			validated := ""
			validate := func(program []byte) error { validated = string(program); return tc.validate(program) }
			repair, err := d.Repair(ProgramRepairRequest{Source: []byte(tc.source), Offered: tc.offered, Validate: validate})
			if err != nil {
				t.Fatal(err)
			}
			if tc.text == "" {
				if repair != nil {
					t.Fatalf("unproven repair offered: %+v", repair)
				}
				return
			}
			want := strings.Replace(tc.source, tc.word, tc.text, 1)
			if repair == nil || repair.Text != tc.text || string(repair.Program) != want || validated != want || repair.Span != d.Span {
				t.Fatalf("repair = %+v validated %q", repair, validated)
			}
			facts := repair.Feedback()
			if facts["replacement_text"] != tc.text || facts["replacement_submission"] != "explicit" || facts["replacement_start_byte"] != d.Span.StartByte || facts["program"] != nil {
				t.Fatalf("repair feedback = %+v", facts)
			}
		})
	}
	syntax := ProgramDiagnostic{Stage: SyntaxDiagnosticStage, Code: "COMMAND_SYNTAX_INVALID", SourceUnitID: "submitted", Span: SourceSpan{StartByte: 5, EndByte: 6, Line: 1, Column: 6}}
	if repair, err := syntax.Repair(ProgramRepairRequest{Source: []byte(`echo "unterminated`), Offered: true, Validate: accept}); repair != nil || err != nil {
		t.Fatal("ambiguous quoting received an invented closure")
	}
	if repair, err := optionDiagnostic("grep --recur x", "--recur").Repair(ProgramRepairRequest{Source: []byte("grep"), Offered: true, Validate: accept}); repair != nil || err != nil {
		t.Fatal("span outside the captured unit was repaired")
	}
}
