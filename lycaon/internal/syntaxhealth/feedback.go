package syntaxhealth

import "fmt"

// FeedbackFacts preserves the snapshot and parser outcome behind a rejection.
func (c Change) FeedbackFacts(phase string) map[string]any {
	data := map[string]any{
		"syntax_transition": c.Transition, "parse_status": c.After.Status,
		"parse_language": c.After.Language, "parse_phase": phase,
		"parse_reason": "syntax_error", "parse_syntax_error": c.After.Status == StatusBroken,
		"parse_timed_out": false, "parse_canceled": false,
	}
	if c.After.Status == StatusClean || c.After.Status == StatusBroken {
		data["after_syntax_burden"] = c.After.Burden
		data["syntax_diagnostics"] = c.After.Diagnostics
		descriptions := make([]string, len(c.After.Diagnostics))
		for i, diagnostic := range c.After.Diagnostics {
			descriptions[i] = diagnostic.Description()
		}
		data["parse_errors"] = descriptions
		data["syntax_diagnostics_truncated"] = c.After.DiagnosticsTruncated
	}
	if c.Before != nil {
		data["before_parse_status"] = c.Before.Status
		if c.Before.Status == StatusClean || c.Before.Status == StatusBroken {
			data["before_syntax_burden"] = c.Before.Burden
		}
	}
	failure := c.After.Failure
	if failure == nil && c.Before != nil && c.Before.Failure != nil {
		failure, phase = c.Before.Failure, "original"
	}
	if failure != nil {
		data["parse_syntax_error"] = false
		for key, value := range failure.Facts(phase) {
			data[key] = value
		}
	}
	return data
}

// Description gives agents a location and the observed syntax fault.
func (d Diagnostic) Description() string {
	kind := "unexpected syntax"
	if d.Kind == "missing" {
		kind = "missing " + d.NodeType
	}
	text := fmt.Sprintf("line %d, column %d: %s", d.Row, d.Col, kind)
	if d.Snippet != "" {
		text += ": " + d.Snippet
	}
	return text
}
