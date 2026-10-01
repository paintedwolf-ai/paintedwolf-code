// Package diag provides a generic structured diagnostic type and a
// source-quoting renderer. It is generalised from Selena's diagnostics.go:
// the caller supplies the Code (e.g. "SEL0001" or "ELIO0003") rather than
// having the package own a code namespace.
package diag

import (
	"bytes"
	"fmt"
	"strings"
)

// Severity classifies the gravity of a diagnostic message.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Diagnostic is a structured compiler/parser message anchored to a source
// position. Line and Col are 1-based; zero values mean "no position".
type Diagnostic struct {
	Code     string   // caller-assigned code, e.g. "SEL0001" or "ELIO0003"
	Severity Severity // defaults to SeverityError when zero
	Line     int      // 1-based source line (0 = unknown)
	Col      int      // 1-based source column (0 = unknown)
	Message  string
	Hint     string // optional; rendered only when non-empty
}

// Render formats d as a human-readable string, quoting the offending source
// line with a caret under the column position.
//
// Example output:
//
//	ELIO0003 error: 1:9 unexpected ';'
//	  let x = ;
//	          ^
//	  hint: provide an expression
func Render(src []byte, d Diagnostic) string {
	sev := d.Severity
	if sev == "" {
		sev = SeverityError
	}

	var b strings.Builder

	// Header line: code + severity + position + message.
	if d.Line > 0 {
		fmt.Fprintf(&b, "%s %s: %d:%d %s\n", d.Code, sev, d.Line, d.Col, d.Message)
	} else {
		fmt.Fprintf(&b, "%s %s: %s\n", d.Code, sev, d.Message)
	}

	// Source-line quote with caret.
	if d.Line > 0 {
		lineText := extractLine(src, d.Line)
		if lineText != "" {
			fmt.Fprintf(&b, "  %s\n", lineText)
			col := d.Col
			if col < 1 {
				col = 1
			}
			// col is 1-based; build padding of (col-1) spaces then ^.
			pad := strings.Repeat(" ", col-1)
			fmt.Fprintf(&b, "  %s^\n", pad)
		}
	}

	// Hint line.
	if d.Hint != "" {
		fmt.Fprintf(&b, "  hint: %s\n", d.Hint)
	}

	return b.String()
}

// extractLine returns the text of the n-th line in src (1-based).
// Returns "" if n is out of range.
func extractLine(src []byte, n int) string {
	lines := bytes.Split(src, []byte("\n"))
	if n < 1 || n > len(lines) {
		return ""
	}
	return string(bytes.TrimRight(lines[n-1], "\r"))
}
