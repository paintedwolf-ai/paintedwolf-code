package toolcontract

import "encoding/json"

// ParserDiagnostic is an observation of parser state, not a source repair.
type ParserDiagnostic struct {
	Kind       string
	Message    string
	Expected   string
	Found      string
	Detected   SourceSpan
	Incomplete bool
	Excerpt    string
	ExcerptAt  SourceSpan
}

func (d ParserDiagnostic) addFacts(facts map[string]any) {
	facts["parser_kind"] = d.Kind
	facts["parser_incomplete"] = d.Incomplete
	if d.Detected.Line > 0 {
		facts["parser_detected_line"], facts["parser_detected_column"] = d.Detected.Line, d.Detected.Column
	}
	for key, value := range map[string]string{"parser_message": d.Message, "parser_expected": d.Expected, "parser_found": d.Found, "source_excerpt": d.Excerpt} {
		if value == "" {
			continue
		}
		encoded, err := json.Marshal(value)
		if err != nil || len(encoded) > 512 {
			facts["parser_details_omitted"] = true
			continue
		}
		facts[key] = value
		if key == "source_excerpt" {
			facts["source_excerpt_start_byte"], facts["source_excerpt_end_byte"] = d.ExcerptAt.StartByte, d.ExcerptAt.EndByte
			facts["source_excerpt_line"], facts["source_excerpt_column"] = d.ExcerptAt.Line, d.ExcerptAt.Column
		}
	}
}
