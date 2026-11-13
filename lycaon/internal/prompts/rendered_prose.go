package prompts

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	orderedItemRe        = regexp.MustCompile(`^([ \t]*)(\d+)\.[ \t]`)
	proseSeparatorCellRe = regexp.MustCompile(`^:?-+:?$`)
)

// orderedItem is the last ordered list item seen at the current position; the
// zero value means no adjacent predecessor.
type orderedItem struct {
	indent string
	num    int
}

// RenderedProseStructureDrift reports rendered-prose structure damage that
// template seams cause: ordered lists whose visible numbering jumps, runs of
// blank lines, and blank lines that cut a table's separator row off from its
// rows. Fence interiors are literal code and are skipped. Numbering is checked
// between immediately adjacent items at the same indent — the seam failure
// shape, where a hidden guard branch pulls later steps next to earlier ones —
// and any blank line or non-item line starts a new block.
func RenderedProseStructureDrift(text string) []string {
	var drift []string
	inFence := false
	prevBlank := false
	blankRunReported := false
	afterSeparator := false
	prev := orderedItem{}
	lines := strings.Split(text, "\n")
	// A trailing newline terminates the last line; the empty split artifact
	// after it is not a blank line.
	if len(lines) > 1 && lines[len(lines)-1] == "" && strings.HasSuffix(text, "\n") {
		lines = lines[:len(lines)-1]
	}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			prevBlank, blankRunReported, afterSeparator, prev = false, false, false, orderedItem{}
			continue
		}
		if inFence {
			continue
		}
		where := fmt.Sprintf("line %d", i+1)
		if trimmed == "" {
			if prevBlank && !blankRunReported {
				drift = append(drift, where+": run of blank lines")
				blankRunReported = true
			}
			if afterSeparator {
				drift = append(drift, where+": blank line after a table separator row")
				afterSeparator = false
			}
			prevBlank = true
			prev = orderedItem{}
			continue
		}
		prevBlank = false
		blankRunReported = false
		afterSeparator = isTableSeparatorRow(trimmed)
		if m := orderedItemRe.FindStringSubmatch(line); m != nil {
			if num, err := strconv.Atoi(m[2]); err == nil {
				if prev.indent == m[1] && prev.num != 0 && num != prev.num+1 {
					drift = append(drift, fmt.Sprintf("%s: ordered list numbering jumps from %d to %d", where, prev.num, num))
				}
				prev = orderedItem{indent: m[1], num: num}
			}
		} else {
			prev = orderedItem{}
		}
	}
	// A blank line beyond the single final newline is the PC03-009 defect
	// class: the text ends with a stranded empty line. Two or more trailing
	// blanks are already reported by the run rule above, so only the
	// exactly-one-trailing-blank shape lands here, and a fence left open at
	// EOF keeps its interior literal.
	if n := len(lines); n >= 2 && !inFence && strings.TrimSpace(lines[n-1]) == "" && strings.TrimSpace(lines[n-2]) != "" {
		drift = append(drift, fmt.Sprintf("line %d: blank line at EOF (single final newline expected)", n))
	}
	return drift
}

// isTableSeparatorRow reports whether a trimmed line is a markdown table
// separator row such as | --- | --- |.
func isTableSeparatorRow(trimmed string) bool {
	if !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") {
		return false
	}
	cells := strings.Split(trimmed, "|")
	if len(cells) < 3 {
		return false
	}
	for _, cell := range cells[1 : len(cells)-1] {
		if !proseSeparatorCellRe.MatchString(strings.TrimSpace(cell)) {
			return false
		}
	}
	return true
}
