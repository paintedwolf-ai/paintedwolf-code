package oarcopy

func publishParserFacts(out map[string]any) {
	kind, _ := out["paintedwolf.parser_kind"].(string)
	for name, value := range map[string]string{
		"parser_unclosed_quote": "unclosed_quote", "parser_unclosed_heredoc": "unclosed_heredoc",
		"parser_expected_closer": "expected_closer", "parser_expected_following": "expected_following",
		"parser_utf8_bom": "utf8_bom", "parser_nul_byte": "nul_byte", "parser_invalid_utf8": "invalid_utf8",
	} {
		out["paintedwolf."+name] = kind == value
	}
	expected, _ := out["paintedwolf.parser_expected"].(string)
	out["paintedwolf.parser_single_quote"] = expected == "'"
}
