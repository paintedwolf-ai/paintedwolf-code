package syntaxhealth

import (
	"strings"
	"unicode/utf8"
)

// CSS permits the legacy <!-- token at stylesheet level, but not markup tags.
// General CSS validation stays unavailable until the grammar supports modern CSS.
func cssMarkupHealth(src []byte) Report {
	text := strings.TrimLeft(string(src), " \t\r\n\f\ufeff")
	report := Report{Language: "css", Status: StatusUnsupported}
	if !strings.HasPrefix(text, "<") || strings.HasPrefix(text, "<!--") {
		return report
	}
	start := len(src) - len(text)
	prefix := string(src[:start])
	row := strings.Count(prefix, "\n") + 1
	last := strings.LastIndex(prefix, "\n")
	col := utf8.RuneCountInString(prefix[last+1:]) + 1
	report.Status = StatusBroken
	report.Burden = Burden{SpanBytes: 1, Faults: 1}
	report.Diagnostics = []Diagnostic{{
		Row: row, Col: col, EndRow: row, EndCol: col + 1,
		StartByte: start, EndByte: start + 1, Snippet: "<",
		Kind: DiagnosticError, NodeType: "invalid_stylesheet_token",
	}}
	return report
}
