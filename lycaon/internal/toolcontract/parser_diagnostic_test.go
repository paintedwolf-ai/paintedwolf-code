package toolcontract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParserDiagnosticBoundsKeepWholeValues(t *testing.T) {
	for _, payload := range []string{strings.Repeat("x", 513), strings.Repeat("\x01", 100), strings.Repeat("é", 300)} {
		facts := map[string]any{}
		ParserDiagnostic{Kind: "unclosed_heredoc", Message: payload, Expected: payload, Found: "EOF", Detected: SourceSpan{Line: 2, Column: 3}}.addFacts(facts)
		if facts["parser_message"] != nil || facts["parser_expected"] != nil || facts["parser_details_omitted"] != true || facts["parser_found"] != "EOF" {
			t.Fatalf("unbounded or partial diagnostic: %#v", facts)
		}
		encoded, err := json.Marshal(facts)
		if err != nil || len(encoded) > 1024 {
			t.Fatalf("encoded diagnostic: %d / %v", len(encoded), err)
		}
	}
}
