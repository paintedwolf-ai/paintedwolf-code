package syntaxhealth

import (
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/runeclamp"
)

const (
	indentPreviewMaxBytes = 80
	linePreviewMaxBytes   = 160
)

// LineContext is a seam-adjacent source line with visible indentation facts.
type LineContext struct {
	Row           int    `json:"row"`
	IndentColumns int    `json:"indent_columns"`
	Indent        string `json:"indent"`
	Text          string `json:"text"`
}

// LineRange is a 1-based inclusive mutation seam.
type LineRange struct {
	Start int
	End   int
}

// NearestDiagnostic returns the localized parser fault closest to the changed
// line range. A zero range selects the first bounded diagnostic.
func NearestDiagnostic(report Report, startLine, endLine int) (Diagnostic, bool) {
	diagnostic, _, ok := NearestDiagnosticForRanges(report, []LineRange{{Start: startLine, End: endLine}})
	return diagnostic, ok
}

// NearestDiagnosticForRanges returns the parser fault closest to any changed
// seam and the seam it matched. Disjoint atomic edits stay disjoint here.
func NearestDiagnosticForRanges(report Report, ranges []LineRange) (Diagnostic, LineRange, bool) {
	if len(report.Diagnostics) == 0 {
		return Diagnostic{}, LineRange{}, false
	}
	if len(ranges) == 0 || ranges[0].Start <= 0 {
		return report.Diagnostics[0], LineRange{}, true
	}
	bestDiagnostic := report.Diagnostics[0]
	bestRange := normalizeLineRange(ranges[0])
	bestDistance := lineDistance(bestDiagnostic.Row, bestRange.Start, bestRange.End)
	bestSpan := bestDiagnostic.EndByte - bestDiagnostic.StartByte
	for _, candidateRange := range ranges {
		candidateRange = normalizeLineRange(candidateRange)
		for _, candidate := range report.Diagnostics {
			distance := lineDistance(candidate.Row, candidateRange.Start, candidateRange.End)
			span := candidate.EndByte - candidate.StartByte
			if distance < bestDistance || distance == bestDistance && span < bestSpan {
				bestDiagnostic, bestRange = candidate, candidateRange
				bestDistance, bestSpan = distance, span
			}
		}
	}
	return bestDiagnostic, bestRange, true
}

func normalizeLineRange(item LineRange) LineRange {
	if item.End < item.Start {
		item.End = item.Start
	}
	return item
}

func lineDistance(row, startLine, endLine int) int {
	if row < startLine {
		return startLine - row
	}
	if row > endLine {
		return row - endLine
	}
	return 0
}

// PythonIndentContext reports indentation near an edit without changing its verdict.
func PythonIndentContext(report Report, src []byte, startLine, endLine int) []LineContext {
	if report.Language != "python" || startLine <= 0 {
		return nil
	}
	lines := strings.Split(string(src), "\n")
	if len(lines) == 0 {
		return nil
	}
	if endLine < startLine {
		endLine = startLine
	}
	from := max(1, startLine-2)
	to := min(len(lines), endLine+2)
	out := make([]LineContext, 0, to-from+1)
	for row := from; row <= to; row++ {
		line := lines[row-1]
		indent, text := splitIndent(line)
		out = append(out, LineContext{
			Row: row, IndentColumns: indentColumns(indent),
			Indent: runeclamp.ClampBytes(visibleIndent(indent), indentPreviewMaxBytes),
			Text:   runeclamp.ClampBytes(text, linePreviewMaxBytes),
		})
	}
	return out
}

func splitIndent(line string) (string, string) {
	idx := 0
	for idx < len(line) {
		r, size := utf8.DecodeRuneInString(line[idx:])
		if r != ' ' && r != '\t' {
			break
		}
		idx += size
	}
	return line[:idx], line[idx:]
}

func indentColumns(indent string) int {
	columns := 0
	for _, r := range indent {
		if r == '\t' {
			columns += 8 - columns%8
		} else {
			columns++
		}
	}
	return columns
}

func visibleIndent(indent string) string {
	return strings.NewReplacer(" ", "·", "\t", "→").Replace(indent)
}
