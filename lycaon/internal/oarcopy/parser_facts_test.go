package oarcopy

import "testing"

func TestParserCopyFactsUseOnlyTypedKinds(t *testing.T) {
	for _, kind := range []string{"unclosed_quote", "unclosed_heredoc", "expected_closer", "expected_following", "utf8_bom", "nul_byte", "invalid_utf8", "unknown", ""} {
		facts := FactsFromData(map[string]any{"parser_kind": kind, "reason": "unclosed_quote", "parser_message": "unclosed_quote", "parser_expected": "'"})
		count := 0
		for _, name := range []string{"parser_unclosed_quote", "parser_unclosed_heredoc", "parser_expected_closer", "parser_expected_following", "parser_utf8_bom", "parser_nul_byte", "parser_invalid_utf8"} {
			if facts["paintedwolf."+name] == true {
				count++
			}
		}
		expected := 1
		if kind == "unknown" || kind == "" {
			expected = 0
		}
		if count != expected || facts["paintedwolf.parser_single_quote"] != true {
			t.Fatalf("kind %q: %#v", kind, facts)
		}
	}
}
